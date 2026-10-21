package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/Bharanipbk/gideondb/internal/embedding"
	"github.com/Bharanipbk/gideondb/internal/rerank"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Bharanipbk/gideondb/internal/api/grpcapi"
	"github.com/Bharanipbk/gideondb/internal/api/rest"
	"github.com/Bharanipbk/gideondb/internal/cluster"
	appconfig "github.com/Bharanipbk/gideondb/internal/config"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/tlsreload"
	"github.com/Bharanipbk/gideondb/internal/wal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var version, commit, buildDate = "dev", "unknown", "unknown"

func main() {
	defaults := appconfig.Default()
	configFile := flag.String("config", "", "strict JSON configuration file")
	address := flag.String("http-address", defaults.HTTPAddress, "REST listen address")
	grpcAddress := flag.String("grpc-address", defaults.GRPCAddress, "gRPC listen address; disabled when empty")
	advertiseAddress := flag.String("advertise-address", defaults.AdvertiseAddress, "address advertised to other nodes; defaults to HTTP address")
	clusterID := flag.String("cluster-id", defaults.ClusterID, "shared 32-character hexadecimal cluster ID; generated and persisted when omitted")
	peers := flag.String("peers", strings.Join(defaults.Peers, ","), "comma-separated static peer base URLs")
	dataPath := flag.String("data-path", defaults.DataPath, "persistent data directory")
	walSync := flag.String("wal-sync", defaults.WALSync, "WAL durability: always or async")
	checkpointEvery := flag.Uint64("checkpoint-every", defaults.CheckpointEvery, "checkpoint a shard after this many mutations; 0 disables")
	replicationFactor := flag.Int("replication-factor", defaults.ReplicationFactor, "number of deterministic shard replicas")
	placementCapacity := flag.Uint("placement-capacity", uint(defaults.PlacementCapacity), "relative shard placement capacity from 1 to 256")
	rateLimitPerSecond := flag.Int("rate-limit-per-second", defaults.RateLimitPerSecond, "public API requests per second per credential or client address")
	rateLimitBurst := flag.Int("rate-limit-burst", defaults.RateLimitBurst, "public API token-bucket burst per credential or client address")
	auditRetention := flag.Int("audit-retention", defaults.AuditRetention, "durable sanitized HTTP audit events retained (256-1000000)")
	apiKeyFile := flag.String("api-key-file", "", "file containing the bearer API key (permissions must be 0600 or stricter)")
	principalsFile := flag.String("principals-file", "", "reloadable JSON file containing API principals, roles, and collection prefixes")
	embeddersFile := flag.String("embedders-file", os.Getenv("GIDEONDB_EMBEDDERS_FILE"), "JSON file of embedding models that collections can use as their vectorizer")
	rerankersFile := flag.String("rerankers-file", os.Getenv("GIDEONDB_RERANKERS_FILE"), "JSON file of cross-encoder rerank endpoints that search requests can name")
	allowUnauthenticated := flag.Bool("allow-unauthenticated", false, "allow an unauthenticated non-loopback HTTP listener")
	allowInsecureHTTP := flag.Bool("allow-insecure-http", false, "allow bearer authentication over cleartext HTTP on a non-loopback listener")
	enableStaticRouting := flag.Bool("enable-static-routing", false, "activate immutable static placement only while all peer views converge")
	tlsCertFile := flag.String("tls-cert-file", "", "TLS certificate chain file")
	tlsKeyFile := flag.String("tls-key-file", "", "TLS private key file")
	tlsSelfSigned := flag.Bool("tls-self-signed", strings.EqualFold(os.Getenv("GIDEONDB_TLS_SELF_SIGNED"), "true"), "serve HTTPS with a self-signed certificate kept in <data-path>/tls when no -tls-cert-file is given (for containers and evaluation)")
	tlsCAFile := flag.String("tls-ca-file", "", "private CA bundle for mutual TLS on internal APIs")
	nodeTLSCertFile := flag.String("node-tls-cert-file", "", "outbound node-client certificate; defaults to tls-cert-file")
	nodeTLSKeyFile := flag.String("node-tls-key-file", "", "outbound node-client private key; defaults to tls-key-file")
	backupTo := flag.String("backup-to", "", "create a consistent backup archive and exit")
	restoreFrom := flag.String("restore-from", "", "restore an archive into the data path and exit (destination must not exist)")
	grpcRestorePath := flag.String("grpc-restore-path", "", "empty destination for administrative gRPC restore streams; disabled when empty")
	verifyData := flag.Bool("verify-data", false, "open and validate the data path, then exit")
	migrateData := flag.Bool("migrate-data", false, "validate and rewrite legacy checkpoints to the current persistent format, then exit")
	validateConfig := flag.Bool("validate-config", false, "validate effective configuration, then exit")
	showVersion := flag.Bool("version", false, "print version information and exit")
	healthcheckURL := flag.String("healthcheck-url", "", "check an HTTP health URL and exit")
	liteMode := flag.Bool("lite", strings.EqualFold(os.Getenv("GIDEONDB_LITE"), "true"), "zero-config single-node mode: loopback only, no authentication or TLS, dashboard off unless credentials are set, data in ~/.gideondb/lite by default")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *showVersion {
		fmt.Printf("gideondb %s commit=%s built=%s\n", version, commit, buildDate)
		return
	}
	if *healthcheckURL != "" {
		client := &http.Client{Timeout: 3 * time.Second}
		// A probe of this process's own loopback listener cannot learn
		// anything from certificate verification, and self-signed
		// container certificates would fail it.
		if target, parseErr := url.Parse(*healthcheckURL); parseErr == nil && target.Scheme == "https" && rest.IsLoopbackAddress(net.JoinHostPort(target.Hostname(), "0")) {
			client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // loopback self-probe only
		}
		response, err := client.Get(*healthcheckURL)
		if err != nil {
			logger.Error("healthcheck failed", "error", err)
			os.Exit(1)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			logger.Error("healthcheck failed", "status", response.StatusCode)
			os.Exit(1)
		}
		return
	}
	settings := defaults
	var configErr error
	if *configFile != "" {
		settings, configErr = appconfig.Load(*configFile, settings)
	}
	if configErr == nil {
		settings, configErr = appconfig.ApplyEnv(settings, os.LookupEnv)
	}
	if configErr != nil {
		logger.Error("load configuration", "error", configErr)
		os.Exit(2)
	}
	flagValues := make(map[string]string)
	flag.Visit(func(item *flag.Flag) { flagValues[item.Name] = item.Value.String() })
	if _, explicit := flagValues["data-path"]; *liteMode && !explicit && settings.DataPath == defaults.DataPath {
		flagValues["data-path"] = liteDefaultDataPath()
	}
	settings, configErr = appconfig.ApplyFlags(settings, flagValues)
	if configErr != nil {
		logger.Error("apply CLI configuration", "error", configErr)
		os.Exit(2)
	}
	if err := settings.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	*address, *grpcAddress, *advertiseAddress, *dataPath, *walSync, *checkpointEvery = settings.HTTPAddress, settings.GRPCAddress, settings.AdvertiseAddress, settings.DataPath, settings.WALSync, settings.CheckpointEvery
	*replicationFactor = settings.ReplicationFactor
	*placementCapacity = uint(settings.PlacementCapacity)
	*rateLimitPerSecond, *rateLimitBurst = settings.RateLimitPerSecond, settings.RateLimitBurst
	*auditRetention = settings.AuditRetention
	*clusterID = settings.ClusterID
	*apiKeyFile, *allowUnauthenticated, *allowInsecureHTTP = settings.APIKeyFile, settings.AllowUnauthenticated, settings.AllowInsecureHTTP
	*principalsFile = settings.PrincipalsFile
	*enableStaticRouting = settings.EnableStaticRouting
	*tlsCertFile, *tlsKeyFile, *tlsCAFile = settings.TLSCertFile, settings.TLSKeyFile, settings.TLSCAFile
	*nodeTLSCertFile, *nodeTLSKeyFile = settings.NodeTLSCertFile, settings.NodeTLSKeyFile
	*peers = strings.Join(settings.Peers, ",")
	offlineActions := 0
	for _, selected := range []bool{*backupTo != "", *restoreFrom != "", *verifyData, *migrateData} {
		if selected {
			offlineActions++
		}
	}
	if offlineActions > 1 {
		logger.Error("-backup-to, -restore-from, -verify-data, and -migrate-data are mutually exclusive")
		os.Exit(2)
	}
	if *restoreFrom != "" {
		if err := engine.RestoreBackup(*restoreFrom, *dataPath); err != nil {
			logger.Error("restore backup", "error", err)
			os.Exit(1)
		}
		logger.Info("backup restored", "source", *restoreFrom, "data_path", *dataPath)
		return
	}
	var apiKey string
	dashboardUsername, dashboardPassword := os.Getenv("GIDEONDB_DASHBOARD_USERNAME"), os.Getenv("GIDEONDB_DASHBOARD_PASSWORD")
	dashboardPasswordFile := os.Getenv("GIDEONDB_DASHBOARD_PASSWORD_FILE")
	dashboardBootstrap := strings.EqualFold(os.Getenv("GIDEONDB_DASHBOARD_BOOTSTRAP"), "true")
	offline := *backupTo != "" || *verifyData || *migrateData
	dashboardEnabled := true
	if *liteMode && !offline {
		if err := checkLite(*address, *grpcAddress, settings.Peers, os.Getenv("GIDEONDB_API_KEY")+*apiKeyFile, *principalsFile); err != nil {
			logger.Error("invalid lite configuration", "error", err)
			os.Exit(2)
		}
		*tlsSelfSigned = false
		_, statErr := os.Stat(filepath.Join(*dataPath, "admin.sqlite"))
		dashboardEnabled = dashboardUsername != "" || dashboardPasswordFile != "" || statErr == nil
	}
	if !offline {
		if *tlsSelfSigned && *tlsCertFile == "" && *tlsKeyFile == "" {
			certPath, keyPath, certErr := ensureSelfSignedCertificate(filepath.Join(*dataPath, "tls"), *address, *advertiseAddress)
			if certErr != nil {
				logger.Error("create self-signed TLS certificate", "error", certErr)
				os.Exit(2)
			}
			*tlsCertFile, *tlsKeyFile = certPath, keyPath
			logger.Warn("serving HTTPS with a self-signed certificate; use -tls-cert-file and -tls-key-file in production", "certificate", certPath)
		}
		if dashboardPassword != "" && dashboardPasswordFile != "" {
			logger.Error("configure only one of GIDEONDB_DASHBOARD_PASSWORD or GIDEONDB_DASHBOARD_PASSWORD_FILE")
			os.Exit(2)
		}
		if dashboardPasswordFile != "" {
			var passwordErr error
			dashboardPassword, passwordErr = rest.LoadDashboardPasswordFile(dashboardPasswordFile)
			if passwordErr != nil {
				logger.Error("load dashboard password file", "error", passwordErr)
				os.Exit(2)
			}
		}
		if _, err := rest.ParseAddress(*address); err != nil {
			logger.Error("invalid HTTP address", "error", err)
			os.Exit(2)
		}
		if (dashboardUsername == "") != (dashboardPassword == "") {
			logger.Error("dashboard username and password must both be configured")
			os.Exit(2)
		}
		_, dashboardCredentialErr := os.Stat(filepath.Join(*dataPath, "admin.sqlite"))
		if os.IsNotExist(dashboardCredentialErr) {
			_, dashboardCredentialErr = os.Stat(filepath.Join(*dataPath, "dashboard-credentials.json"))
		}
		if dashboardEnabled && dashboardUsername == "" && os.IsNotExist(dashboardCredentialErr) {
			logger.Error("new data directory requires GIDEONDB_DASHBOARD_USERNAME and a dashboard password source")
			os.Exit(2)
		}
		if dashboardBootstrap && !rest.IsLoopbackAddress(*address) {
			logger.Error("GIDEONDB_DASHBOARD_BOOTSTRAP is restricted to loopback listeners")
			os.Exit(2)
		}
		if !rest.IsLoopbackAddress(*address) && *tlsCertFile == "" {
			logger.Error("non-loopback dashboard authentication requires HTTPS", "hint", "configure -tls-cert-file and -tls-key-file")
			os.Exit(2)
		}
		if *grpcAddress != "" {
			if _, err := rest.ParseAddress(*grpcAddress); err != nil {
				logger.Error("invalid gRPC address", "error", err)
				os.Exit(2)
			}
		}
		if *advertiseAddress == "" {
			*advertiseAddress = *address
		}
		if _, err := rest.ParseAddress(*advertiseAddress); err != nil {
			logger.Error("invalid advertise address", "error", err)
			os.Exit(2)
		}
		if *apiKeyFile != "" {
			loaded, keyErr := rest.LoadAPIKeyFile(*apiKeyFile)
			if keyErr != nil {
				logger.Error("load API key", "error", keyErr)
				os.Exit(2)
			}
			apiKey = loaded
		}
		// GIDEONDB_API_KEY suits containers, where mounting a 0600 file owned
		// by the non-root user is awkward. It is visible to anyone who can
		// inspect the process environment, so prefer -api-key-file.
		if environmentKey := strings.TrimSpace(os.Getenv("GIDEONDB_API_KEY")); environmentKey != "" {
			if *apiKeyFile != "" {
				logger.Error("configure only one of -api-key-file or GIDEONDB_API_KEY")
				os.Exit(2)
			}
			if len(environmentKey) < 16 {
				logger.Error("GIDEONDB_API_KEY must contain at least 16 characters")
				os.Exit(2)
			}
			apiKey = environmentKey
		}
		if *principalsFile != "" {
			if _, principalsErr := rest.LoadPrincipalsFile(*principalsFile); principalsErr != nil {
				logger.Error("load principals", "error", principalsErr)
				os.Exit(2)
			}
		}
		if apiKey == "" && *principalsFile == "" && !rest.IsLoopbackAddress(*address) && !*allowUnauthenticated {
			logger.Error("refusing unauthenticated non-loopback listener", "address", *address, "hint", "configure -api-key-file or explicitly set -allow-unauthenticated")
			os.Exit(2)
		}
		if (apiKey != "" || *principalsFile != "") && !rest.IsLoopbackAddress(*address) && *tlsCertFile == "" && !*allowInsecureHTTP {
			logger.Error("refusing bearer authentication over non-loopback cleartext HTTP", "hint", "configure TLS or explicitly set -allow-insecure-http")
			os.Exit(2)
		}
		if *grpcAddress != "" && apiKey == "" && *principalsFile == "" && !rest.IsLoopbackAddress(*grpcAddress) && !*allowUnauthenticated {
			logger.Error("refusing unauthenticated non-loopback gRPC listener", "address", *grpcAddress, "hint", "configure -api-key-file or explicitly set -allow-unauthenticated")
			os.Exit(2)
		}
		if *grpcAddress != "" && (apiKey != "" || *principalsFile != "") && !rest.IsLoopbackAddress(*grpcAddress) && *tlsCertFile == "" && !*allowInsecureHTTP {
			logger.Error("refusing bearer authentication over non-loopback cleartext gRPC", "hint", "configure TLS or explicitly set -allow-insecure-http")
			os.Exit(2)
		}
	}
	if *validateConfig {
		logger.Info("configuration valid")
		return
	}
	var internalClient *http.Client
	var serverTLSConfig *tls.Config
	if *tlsCertFile != "" {
		serverFiles := tlsreload.Files{Cert: *tlsCertFile, Key: *tlsKeyFile, CA: *tlsCAFile}
		var tlsErr error
		serverTLSConfig, tlsErr = tlsreload.ServerConfig(serverFiles)
		if tlsErr != nil {
			logger.Error("load server TLS identity", "error", tlsErr)
			os.Exit(2)
		}
	}
	if *tlsCAFile != "" {
		clientCert, clientKey := *nodeTLSCertFile, *nodeTLSKeyFile
		if clientCert == "" {
			clientCert, clientKey = *tlsCertFile, *tlsKeyFile
		}
		clientTLSConfig, configErr := tlsreload.ClientConfig(tlsreload.Files{Cert: clientCert, Key: clientKey, CA: *tlsCAFile})
		if configErr != nil {
			logger.Error("load node-client TLS identity", "error", configErr)
			os.Exit(2)
		}
		internalClient = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: clientTLSConfig}}
	}
	db, err := engine.OpenWithOptions(*dataPath, engine.Options{
		WALSyncMode: wal.SyncMode(*walSync), CheckpointEvery: *checkpointEvery,
	})
	if err != nil {
		logger.Error("open engine", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("close engine", "error", err)
		}
	}()
	dashboardCredentialsPath := filepath.Join(*dataPath, "admin.sqlite")
	if !dashboardEnabled {
		dashboardCredentialsPath = ""
	}
	if !offline && dashboardEnabled {
		if err := rest.PrepareDashboardCredentials(dashboardCredentialsPath, dashboardUsername, dashboardPassword, dashboardBootstrap); err != nil {
			logger.Error("prepare dashboard credentials", "error", err)
			os.Exit(2)
		}
	}
	if *backupTo != "" {
		if err := db.Backup(*backupTo); err != nil {
			logger.Error("create backup", "error", err)
			os.Exit(1)
		}
		logger.Info("backup created", "destination", *backupTo)
		return
	}
	if *verifyData {
		logger.Info("data verification complete", "data_path", *dataPath)
		return
	}
	if *migrateData {
		if err := db.MigratePersistentFormats(); err != nil {
			logger.Error("migrate persistent formats", "error", err)
			os.Exit(1)
		}
		logger.Info("persistent format migration complete", "data_path", *dataPath, "manifest_format", 3)
		return
	}
	identity, err := cluster.LoadOrCreate(*dataPath)
	if err != nil {
		logger.Error("load node identity", "error", err)
		os.Exit(1)
	}
	clusterMetadata, err := cluster.LoadOrCreateMetadata(*dataPath, *clusterID)
	if err != nil {
		logger.Error("load cluster metadata", "error", err)
		os.Exit(1)
	}
	raftStore, err := cluster.OpenRaftStore(*dataPath, identity.ID, clusterMetadata.Epoch)
	if err != nil {
		logger.Error("load metadata Raft state", "error", err)
		os.Exit(1)
	}
	rebalanceBarriers, err := cluster.OpenRebalanceBarrierStore(*dataPath)
	if err != nil {
		logger.Error("load rebalance write barriers", "error", err)
		os.Exit(1)
	}
	rebalanceExecutor, err := cluster.OpenRebalanceExecutor(*dataPath)
	if err != nil {
		logger.Error("load rebalance execution journal", "error", err)
		os.Exit(1)
	}
	_, _, metadataEpoch := raftStore.State()
	discovery, err := cluster.NewDiscovery(identity.ID, clusterMetadata.ClusterID, settings.Peers, apiKey, internalClient)
	if err != nil {
		logger.Error("configure peer discovery", "error", err)
		os.Exit(2)
	}
	raftRuntime, err := cluster.NewRaftRuntime(raftStore, identity.ID, func() []cluster.RaftPeer {
		peers := discovery.Peers()
		result := make([]cluster.RaftPeer, 0, len(peers))
		for _, peer := range peers {
			result = append(result, cluster.RaftPeer{NodeID: peer.NodeID, BaseURL: peer.SeedURL})
		}
		return result
	}, &cluster.HTTPRaftTransport{ClusterID: clusterMetadata.ClusterID, APIKey: apiKey, Client: internalClient}, cluster.RaftRuntimeConfig{})
	if err != nil {
		logger.Error("configure metadata Raft runtime", "error", err)
		os.Exit(2)
	}
	var embedders *embedding.Registry
	if *embeddersFile != "" {
		var embeddersErr error
		if embedders, embeddersErr = embedding.LoadFile(*embeddersFile); embeddersErr != nil {
			logger.Error("load embedders", "error", embeddersErr)
			os.Exit(2)
		}
	}
	var rerankers *rerank.Registry
	if *rerankersFile != "" {
		var rerankersErr error
		if rerankers, rerankersErr = rerank.LoadFile(*rerankersFile); rerankersErr != nil {
			logger.Error("load rerankers", "error", rerankersErr)
			os.Exit(2)
		}
	}
	apiServer := rest.NewWithOptions(db, logger, rest.Options{APIKey: apiKey, PrincipalsFile: *principalsFile, NodeID: identity.ID, ClusterID: clusterMetadata.ClusterID, AdvertiseAddress: *advertiseAddress, EventLogPath: filepath.Join(*dataPath, "operational-events.jsonl"), EventLogRetention: *auditRetention, MetadataEpoch: metadataEpoch, ReplicationFactor: *replicationFactor, PlacementCapacity: uint32(*placementCapacity), PeerProvider: discovery, InternalHTTPClient: internalClient, EnableStaticRouting: *enableStaticRouting, RaftStore: raftStore, RaftProtocol: raftRuntime, RebalanceBarriers: rebalanceBarriers, RebalanceExecutor: rebalanceExecutor, RequireInternalMTLS: *tlsCAFile != "", RateLimitPerSecond: *rateLimitPerSecond, RateLimitBurst: *rateLimitBurst, DashboardCredentialsFile: dashboardCredentialsPath, Embedders: embedders, Rerankers: rerankers})
	server := &http.Server{
		Addr: *address, Handler: apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	server.TLSConfig = serverTLSConfig
	var grpcServer *grpc.Server
	var grpcListener net.Listener
	if *grpcAddress != "" {
		grpcListener, err = net.Listen("tcp", *grpcAddress)
		if err != nil {
			logger.Error("listen for gRPC", "address", *grpcAddress, "error", err)
			os.Exit(1)
		}
		grpcOptions := make([]grpc.ServerOption, 0, 1)
		if serverTLSConfig != nil {
			grpcOptions = append(grpcOptions, grpc.Creds(credentials.NewTLS(serverTLSConfig.Clone())))
		}
		var principalResolver func(string) (*grpcapi.Principal, error)
		if *principalsFile != "" {
			principalResolver = func(token string) (*grpcapi.Principal, error) {
				principal, err := rest.AuthenticatePrincipalFile(*principalsFile, token)
				if err != nil || principal == nil {
					return nil, err
				}
				return &grpcapi.Principal{Name: principal.Name, Role: principal.Role, CollectionPrefixes: append([]string(nil), principal.CollectionPrefixes...)}, nil
			}
		}
		grpcServer = grpcapi.NewServerWithOptions(db, grpcapi.Options{
			APIKey: apiKey, NodeID: identity.ID, ClusterID: clusterMetadata.ClusterID,
			AdvertiseAddress: *advertiseAddress, MetadataEpoch: metadataEpoch,
			ReplicationFactor: *replicationFactor, PlacementCapacity: uint32(*placementCapacity),
			EnableStaticRouting: *enableStaticRouting, PeerProvider: discovery,
			RaftStore: raftStore, RaftStatus: raftRuntime, PrincipalResolver: principalResolver,
			RateLimitPerSecond: *rateLimitPerSecond, RateLimitBurst: *rateLimitBurst,
			AuditRecorder: apiServer, DistributedSearch: apiServer.DistributedSearchHandler(),
			DistributedScroll:  apiServer.DistributedScrollHandler(),
			DistributedUpsert:  apiServer.DistributedBatchUpsertHandler(),
			DistributedDelete:  apiServer.DistributedDeleteHandler(),
			RestoreDirectory:   *grpcRestorePath,
			ClusterSnapshotter: apiServer,
			ServerOptions:      grpcOptions,
			Embedders:          embedders,
		})
		defer grpcServer.Stop()
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go discovery.Run(runContext, 5*time.Second)
	go raftRuntime.Run(runContext)
	go apiServer.RunReplicaRepair(runContext, 10*time.Second)
	go func() {
		<-signalContext.Done()
		apiServer.BeginDrain()
		transferContext, cancelTransfer := context.WithTimeout(context.Background(), 10*time.Second)
		if err := raftRuntime.TransferLeadership(transferContext); err != nil {
			logger.Warn("leadership transfer before shutdown did not complete", "error", err)
		}
		cancelTransfer()
		cancelRun()
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		_ = server.Shutdown(shutdownContext)
		if grpcServer != nil {
			grpcStopped := make(chan struct{})
			go func() { grpcServer.GracefulStop(); close(grpcStopped) }()
			select {
			case <-grpcStopped:
			case <-shutdownContext.Done():
				grpcServer.Stop()
			}
		}
	}()
	if grpcServer != nil {
		go func() {
			logger.Info("gRPC server starting", "address", *grpcAddress, "tls", serverTLSConfig != nil)
			if err := grpcServer.Serve(grpcListener); err != nil {
				logger.Error("gRPC server stopped", "error", err)
				stop()
			}
		}()
	}
	logger.Info("server starting", "address", *address, "grpc_address", *grpcAddress, "advertise_address", *advertiseAddress, "node_id", identity.ID, "cluster_id", clusterMetadata.ClusterID, "metadata_epoch", metadataEpoch, "peers", len(settings.Peers), "static_routing", *enableStaticRouting, "replication_factor", *replicationFactor, "data_path", *dataPath)
	if *liteMode {
		printLiteBanner(os.Stderr, version, *address, *grpcAddress, *dataPath, dashboardEnabled)
	}
	var serveErr error
	if *tlsCertFile != "" {
		serveErr = server.ListenAndServeTLS("", "")
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		logger.Error("server stopped", "error", serveErr)
		os.Exit(1)
	}
}
