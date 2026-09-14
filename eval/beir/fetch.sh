#!/bin/sh
# Fetches the BEIR development-subset datasets used by the Mousa evaluation
# harness and verifies their pinned SHA-256 digests. Datasets are downloaded to
# the caller's directory and are never committed to the repository.
#
# Usage: ./fetch.sh <target-directory>
# Requires: curl (or wget), unzip, sha256sum
set -eu

BASE_URL="https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets"
# Pin a specific download; re-pin only with a recorded reason in the research
# record. Digests below are for the 2021-04-21 BEIR dataset drops.
SCIFACT_SHA256="536e14446a0ba56ed1398ab1055f39fe852686ecad24a6306c80c490fa8e0165"
NFCORPUS_SHA256="efe5be03f8c5b86a5870102d0599d227c8c6e2484328e68c6522560385671b0b"
ARGUANA_SHA256="cfdf79adce27a401b3cd3ea267903134dbfab2c6afeb95d7fe5724a00bf7557b"

target=${1:?usage: fetch.sh <target-directory>}
mkdir -p "$target"

fetch() {
	name=$1
	sha256_expected=$2
	archive="$target/$name.zip"
	if [ -f "$target/$name/corpus.jsonl" ] && [ -f "$target/$name/queries.jsonl" ]; then
		echo "$name: already present"
		return
	fi
	if [ ! -f "$archive" ]; then
		echo "$name: downloading"
		if command -v curl >/dev/null 2>&1; then
			curl -fsSL -o "$archive" "$BASE_URL/$name.zip"
		else
			wget -q -O "$archive" "$BASE_URL/$name.zip"
		fi
	fi
	echo "$sha256_expected  $archive" | sha256sum -c - >/dev/null || {
		echo "$name: sha256 mismatch" >&2
		exit 1
	}
	unzip -q -o "$archive" -d "$target"
	echo "$name: ok"
}

fetch scifact "$SCIFACT_SHA256"
fetch nfcorpus "$NFCORPUS_SHA256"
fetch arguana "$ARGUANA_SHA256"
