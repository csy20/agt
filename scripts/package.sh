#!/bin/sh
# Build unsigned local archives under dist/. Do not publish them.
# linux/amd64 is executed with `agt version`. Every other target is
# cross-compiled and is not executed. This repository has no LICENSE file.
set -eu
cd "$(dirname "$0")/.."

ver=$(sed -n 's/^[[:space:]]*Product = "\([^"]*\)"/\1/p' internal/version/version.go)
if [ -z "$ver" ]; then
	echo "could not read version.Product" >&2
	exit 1
fi

rm -rf dist
mkdir -p dist/stage
note="agt ${ver}
Unsigned local build. Not published.
This repository has no LICENSE file and is not presented as licensed.
Only the linux/amd64 agt binary is executed, and only to print its version.
Other archives are cross-compiled and were not executed.
Setup, upgrade, and uninstall are in docs/beta.md of the source tree.
This build did not call a hosted model.
"

build_one() {
	goos=$1
	goarch=$2
	name="agt_${ver}_${goos}_${goarch}"
	stage="dist/stage/${name}"
	mkdir -p "$stage"
	printf '%s' "$note" >"$stage/README.txt"
	if [ "$goos" = "windows" ]; then
		CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$goos" GOARCH="$goarch" go build -buildvcs=false -o "$stage/agt.exe" ./cmd/agt
		CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$goos" GOARCH="$goarch" go build -buildvcs=false -o "$stage/agtd.exe" ./cmd/agtd
		(cd "$stage" && zip -q "../../${name}.zip" agt.exe agtd.exe README.txt)
	else
		CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$goos" GOARCH="$goarch" go build -buildvcs=false -o "$stage/agt" ./cmd/agt
		CGO_ENABLED=0 GOTOOLCHAIN=local GOOS="$goos" GOARCH="$goarch" go build -buildvcs=false -o "$stage/agtd" ./cmd/agtd
		tar -C "$stage" -czf "dist/${name}.tar.gz" agt agtd README.txt
	fi
}

build_one linux amd64
got=$(dist/stage/agt_${ver}_linux_amd64/agt version)
if [ "$got" != "$ver" ]; then
	echo "linux/amd64 agt version printed ${got}, want ${ver}" >&2
	exit 1
fi

build_one linux arm64
build_one darwin amd64
build_one darwin arm64
build_one windows amd64

{
	echo "version ${ver}"
	echo "built $(date -u +%Y-%m-%dT%H:%M:%SZ) on $(uname -sm)"
	echo "$(go version)"
	echo "linux/amd64: built for this host and executed only as 'agt version' -> ${got}"
	echo "linux/arm64: cross-compiled, not executed"
	echo "darwin/amd64: cross-compiled, not executed"
	echo "darwin/arm64: cross-compiled, not executed"
	echo "windows/amd64: cross-compiled, not executed"
	echo "license: no LICENSE file in the repository; these archives are not presented as licensed"
	echo "hosted model: not called"
} >dist/BUILD.txt

(
	cd dist
	sha256sum agt_${ver}_*.tar.gz agt_${ver}_*.zip >SHA256SUMS
)
rm -rf dist/stage
echo "wrote dist/ for ${ver}"
