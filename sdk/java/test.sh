#!/bin/sh
set -eu
output="${TMPDIR:-/tmp}/gideondb-java-sdk-classes"
rm -rf "$output"
mkdir -p "$output"
javac -Xlint:all -Werror -d "$output" \
  src/main/java/io/gideondb/client/GideonDBClient.java \
  src/test/java/io/gideondb/client/GideonDBClientTest.java
java -ea -cp "$output" io.gideondb.client.GideonDBClientTest
rm -rf "$output"
