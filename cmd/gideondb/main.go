package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Bharanipbk/gideondb/internal/api/rest"
	"github.com/Bharanipbk/gideondb/internal/cluster"
	appconfig "github.com/Bharanipbk/gideondb/internal/config"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/tlsreload"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

var version, commit, buildDate = "dev", "unknown", "unknown"

func main() {
	defaults := appconfig.Default()
	configFile := flag.String("config", "", "strict JSON configuration file")
	address := flag.String("http-address", defaults.HTTPAddress, "REST listen address")
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
	allowUnauthenticated := flag.Bool("allow-unauthenticated", false, "allow an unauthenticated non-loopback HTTP listener")
	allowInsecureHTTP := flag.Bool("allow-insecure-http", false, "allow bearer authentication over cleartext HTTP on a non-loopback listener")
	enableStaticRouting := flag.Bool("enable-static-routing", false, "activate immutable static placement only while all peer views converge")
	tlsCertFile := flag.String("tls-cert-file", "", "TLS certificate chain file")
	tlsKeyFile := flag.String("tls-key-file", "", "TLS private key file")
	tlsCAFile := flag.String("tls-ca-file", "", "private CA bundle for mutual TLS on internal APIs")
	nodeTLSCertFile := flag.String("node-tls-cert-file", "", "outbound node-client certificate; defaults to tls-cert-file")
	nodeTLSKeyFile := flag.String("node-tls-key-file", "", "outbound node-client private key; defaults to tls-key-file")
	backupTo := flag.String("backup-to", "", "create a consistent backup archive and exit")
	restoreFrom := flag.String("restore-from", "", "restore an archive into the data path and exit (destination must not exist)")
	verifyData := flag.Bool("verify-data", false, "open and validate the data path, then exit")
	migrateData := flag.Bool("migrate-data", false, "validate and rewrite legacy checkpoints to the current persistent format, then exit")
	validateConfig := flag.Bool("validate-config", false, "validate effective configuration, then exit")
	showVersion := flag.Bool("version", false, "print version information and exit")
	healthcheckURL := flag.String("healthcheck-url", "", "check an HTTP health URL and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *showVersion {
		fmt.Printf("gideondb %s commit=%s built=%s\n", version, commit, buildDate)
		return
	}
	if *healthcheckURL != "" {
		client := &http.Client{Timeout: 3 * time.Second}
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
	settings, configErr = appconfig.ApplyFlags(settings, flagValues)
	if configErr != nil {
		logger.Error("apply CLI configuration", "error", configErr)
		os.Exit(2)
	}
	if err := settings.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	*address, *advertiseAddress, *dataPath, *walSync, *checkpointEvery = settings.HTTPAddress, settings.AdvertiseAddress, settings.DataPath, settings.WALSync, settings.CheckpointEvery
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
	offline := *backupTo != "" || *verifyData || *migrateData
	if !offline {
		if _, err := rest.ParseAddress(*address); err != nil {
			logger.Error("invalid HTTP address", "error", err)
			os.Exit(2)
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
	}
	if *validateConfig {
		logger.Info("configuration valid")
		return
	}
	var internalClient *http.Client
	var serverTLSConfig *tls.Config
	if *tlsCAFile != "" {
		serverFiles := tlsreload.Files{Cert: *tlsCertFile, Key: *tlsKeyFile, CA: *tlsCAFile}
		var tlsErr error
		serverTLSConfig, tlsErr = tlsreload.ServerConfig(serverFiles)
		if tlsErr != nil {
			logger.Error("load server TLS identity", "error", tlsErr)
			os.Exit(2)
		}
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
	apiServer := rest.NewWithOptions(db, logger, rest.Options{APIKey: apiKey, PrincipalsFile: *principalsFile, NodeID: identity.ID, ClusterID: clusterMetadata.ClusterID, AdvertiseAddress: *advertiseAddress, EventLogPath: filepath.Join(*dataPath, "operational-events.jsonl"), EventLogRetention: *auditRetention, MetadataEpoch: metadataEpoch, ReplicationFactor: *replicationFactor, PlacementCapacity: uint32(*placementCapacity), PeerProvider: discovery, InternalHTTPClient: internalClient, EnableStaticRouting: *enableStaticRouting, RaftStore: raftStore, RaftProtocol: raftRuntime, RebalanceBarriers: rebalanceBarriers, RebalanceExecutor: rebalanceExecutor, RequireInternalMTLS: *tlsCAFile != "", RateLimitPerSecond: *rateLimitPerSecond, RateLimitBurst: *rateLimitBurst})
	server := &http.Server{
		Addr: *address, Handler: apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	server.TLSConfig = serverTLSConfig

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
	}()
	logger.Info("server starting", "address", *address, "advertise_address", *advertiseAddress, "node_id", identity.ID, "cluster_id", clusterMetadata.ClusterID, "metadata_epoch", metadataEpoch, "peers", len(settings.Peers), "static_routing", *enableStaticRouting, "replication_factor", *replicationFactor, "data_path", *dataPath)
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
