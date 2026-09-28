# WS5 — Integration, build, demo (notes)

This captures what WS5 verified and the fast loop the whole team can reuse.

## Environment (single-ASIC SONiC VS)

Validated on a running SONiC master VS (`vlab-01`, Debian 13.6, glibc 2.41):

- Record files present and populated: `/var/log/swss/{swss.rec,sairedis.rec}` plus
  rotated `.1` and `.2.gz .. .5.gz`, and `retry.rec`.
- The gnmi container mounts host `/` read-only at `/mnt/host`, so the files are at
  `/mnt/host/var/log/swss/` — no new mount needed. `/etc/localtime` is mounted, so
  the container TZ matches the host (UTC on this box).
- Single namespace only (`localhost`); `asicN` stays unit-test-only per plan.

## Findings that shape the plan

1. **`synchronous_mode` is enabled, but there are zero `|E|` records** on the box,
   and attempts to force one (bogus nexthops, in-use deletes) produced none. The
   VS's virtual SAI never fails, so failure records cannot be produced naturally on
   a VS. `ops=E` is validated with a hand-written fixture
   (`testdata/records/sairedis_with_E.sample`) and injection.
2. **Real `sairedis.rec` is richer than `ts|op|key|attr`:** there are API-name
   variants (`q|attribute_capability|...`, `q|object_type_get_availability|...`) and
   bulk lines using `||` separators with the object type stated once
   (`S|SAI_OBJECT_TYPE_ROUTE_ENTRY||{k1}|a1||{k2}|a2...`). The parser keys off the
   `SAI_OBJECT_TYPE_` token to find the key regardless of variant.
3. **Toolchain/ABI skew:** the running VS is fresh master (trixie, libhiredis.so.1,
   python3.13, **libyang.so.3**). Older local build trees (upscale
   `nbi/translib-unify`, go 1.21) vendor a CVL that uses the libyang **1.x** API and
   will not compile against libyang3 headers. A binary that runs *inside* the VS
   gnmi container needs a libyang3-compatible source (fresh master mgmt-common).
   This is a build-infra follow-up, not a code issue in this PR.
4. **telemetry flag parsing:** `main()` parses telemetry flags via a private
   `flag.FlagSet` that rejects unknown flags, so `-records_dir` on the CLI is not
   accepted. RECORDS is therefore configured via env (`RECORDS_DIR`, `RECORDS_TZ`).

## Fast build loop (~35s warm)

Build the telemetry binary against a warm vendor tree inside a sonic-slave
container, then `docker cp` it into the gnmi container and restart:

```sh
# inside sonic-slave-<debian> with the repo + a warm vendor/ at /work/sonic-gnmi
GO=/work/go-dist/go/bin/go
export GOPATH=/work/gopath GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
export CGO_CFLAGS="-I/usr/include/swss" CGO_LDFLAGS="-lswsscommon -lhiredis"
cd /work/sonic-gnmi
$GO build -o build/bin/telemetry -mod=vendor -tags "gnmi_translib_write" \
   github.com/sonic-net/sonic-gnmi/telemetry

# deploy to the VS gnmi container
docker cp gnmi:/usr/sbin/telemetry /usr/sbin/telemetry.orig   # backup once
docker cp build/bin/telemetry gnmi:/usr/sbin/telemetry
docker exec gnmi supervisorctl restart gnmi-native
```

Match the sonic-slave Debian release to the target gnmi container (glibc /
libhiredis / libpython / libyang sonames must match).

## Demo (proven end-to-end on the VS's real data)

See `doc/records_demo.sh`. Verified: replay with `from=`, exact key match,
APPL_DB→sairedis correlation (`matched_by:correlation:ROUTE_TABLE.dest`),
`ops=DEL`/`ops=E` filters, and live tail of a `config route add/del`.
