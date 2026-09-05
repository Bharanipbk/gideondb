#!/bin/sh
set -eu
output="${TMPDIR:-/tmp}/vectordb-java-sdk-classes"
rm -rf "$output"
mkdir -p "$output"
javac -Xlint:all -Werror -d "$output" \
  src/main/java/io/vectordb/client/VectorDBClient.java \
  src/test/java/io/vectordb/client/VectorDBClientTest.java
java -ea -cp "$output" io.vectordb.client.VectorDBClientTest
rm -rf "$output"
