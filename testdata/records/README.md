# RECORDS golden samples (WS5 → WS3/WS4 handoff)

Real recorder-file samples collected from a running single-ASIC SONiC VS
(`vlab-01`, SONiC master, trixie) for parser/matcher development and golden
tests. These are the *actual* on-wire formats — write parsers against these, not
the idealized `ts|op|key|attr` sketch in the plan.

| File | Source | Contents |
|---|---|---|
| `swss.rec.sample` | live `/var/log/swss/swss.rec` (tail 200) | APPL_DB ops: `ROUTE_TABLE`, `NEIGH_TABLE`, ... `ts\|TABLE:key\|SET/DEL\|f:v...` |
| `sairedis.rec.sample` | live `/var/log/swss/sairedis.rec` (tail 200) | sairedis ops incl. bulk `\|\|` and api-name variants |
| `sairedis.rec.2.gz` | gzip of `/var/log/swss/sairedis.rec` (first 4000 lines) | real compressed content for gz-replay tests: `c/s/r` (create/set/remove), `C/S/R/B` bulk, `g/G` get+response, `q/Q` query+response, `n` notify |
| `retry.rec.sample` | live `/var/log/swss/retry.rec` | orchagent retry recorder (banners only on this box) |
| `sairedis_with_E.sample` | **hand-written** | synthetic failure fixture (see note) |

## Real sairedis format notes (important for WS3)

- Standard op: `ts|<op>|SAI_OBJECT_TYPE_X:<oid or {json}>|attr=val|...`
  - single ops are lowercase: `c` create, `r` remove, `s` set, `g` get, `p` counter-poll
- API-name variants (get/query): `ts|q|<api>|SAI_OBJECT_TYPE_X:...|...` and the
  response `ts|Q|<api>|SAI_STATUS_...|...`. The key token is the first field that
  starts with `SAI_OBJECT_TYPE_`; anything before it is the API name.
- Bulk ops are uppercase with `||` separators and the object type stated once:
  `ts|C|SAI_OBJECT_TYPE_ROUTE_ENTRY||{k1}|a1||{k2}|a2...` (same for `R`, `S`, `B`).
- Responses that carry a status: `G|<status>|...` (get), `Q|<api>|<status>|...`
  (query), `F|<status>` (flush fdb), `A|<status>`. Non-success values seen on this
  box: `SAI_STATUS_NOT_SUPPORTED`, `SAI_STATUS_NOT_IMPLEMENTED`,
  `SAI_STATUS_BUFFER_OVERFLOW` — all on `Q` capability probes, not programming ops.

## The `E` (failure) line — why it's hand-written

The plan assumes create/set/remove failures appear as `ts|E|SAI_STATUS_...`.
**Current sonic-sairedis emits no `E` opcode at all**, and its create/set/remove
response recorders (`recordGenericCreateResponse` etc. in `lib/Recorder.cpp`) are
empty `// TODO` stubs — so a failed route/neighbor program is **not recorded** in
`sairedis.rec` on any box today (VS or hardware). Only get/query responses carry a
status. A virtual switch additionally never returns SAI failures (vslib accepts
everything), so we cannot capture a real `E` here.

`sairedis_with_E.sample` is therefore a hand-written fixture (per the plan's
Risk-#2 mitigation) so the `ops=E` / status-attachment path is testable either way.
When wiring real failure detection, read non-`SUCCESS` status off the `G`/`Q`/`F`/`A`
response lines rather than expecting an `E` opcode.
