# go-opcda

Go bindings for the OPC Classic COM/DCOM interfaces, generated from the
original OPC IDL definitions and built on top of
[oiweiwei/go-msrpc](https://github.com/oiweiwei/go-msrpc).

## What is OPC?

OPC is a family of interoperability standards for industrial automation. It
defines standardized client/server interfaces so that data-source devices
(PLCs, fieldbus equipment) and consuming software (SCADA, HMI, historians) from
different vendors can exchange real-time data without a custom integration for
every pairing. The acronym originally stood for "OLE for Process Control" and
was later broadened to "Open Platform Communications". The specifications are
maintained by the OPC Foundation (https://opcfoundation.org).

This project targets **OPC Classic**, the original family of OPC specifications
that is built on Microsoft COM/DCOM. On the wire, remote OPC Classic traffic is
DCOM, which is layered on MS-RPC (Microsoft's extension of DCE/RPC). Because
go-msrpc provides a pure-Go implementation of MS-RPC and DCOM, this library can
speak to OPC Classic servers without depending on Windows COM.

Note: OPC Classic is distinct from the newer, platform-independent OPC UA
(Unified Architecture), which does not use COM/DCOM and is not covered here.

## Command-line client (`gopcda`)

`cmd/gopcda` is a ready-to-use OPC DA client built on these bindings. It
remotely activates an OPC DA server over DCOM and performs the everyday
operations of a SCADA/HMI/historian front-end: read server status, browse the
address space, inspect item properties, and read/write item values - all from
Linux/macOS/Windows, without a local COM stack.

Authentication and targeting are provided by
[RedTeamPentesting/adauth](https://github.com/RedTeamPentesting/adauth), so it
takes the same credential flags as the
[`wmiq`](https://github.com/oiweiwei/wmiq) tool: username/password, NT hashes,
Kerberos (ccache / AES key), PFX client certificates, and SOCKS5 proxying.

### Install

```sh
go install github.com/oiweiwei/go-opcda/cmd/gopcda@latest
# or, from a checkout:
go build -o gopcda ./cmd/gopcda
```

Pre-built binaries for Linux/macOS/Windows are published on the releases page
(built with GoReleaser on tag push).

> The CLI's dependencies (adauth, cobra, ...) live in the single module
> `go.mod`, but they are only imported by `cmd/gopcda`. Thanks to Go module
> graph pruning, importing just the `opc/...` library packages does not pull
> them into your build.

### Synopsis

```
gopcda [global flags] <command> <target> [command flags] [items...]
```

`<target>` is the host running the OPC server (e.g. `dc01.msad.local` or
`192.168.56.10`). Commands: `status`, `browse`, `props`, `read`, `write`.

### Global flags

| Flag | Description |
|---|---|
| `-u, --user` | Username (`user@domain`, `domain\user`, `domain/user`, or `user`) |
| `-p, --password` | Password |
| `-H, --nt-hash` | NT hash (`NT`, `:NT`, or `LM:NT`) |
| `-k, --kerberos` | Use Kerberos authentication |
| `--ccache` | Kerberos ccache file (defaults to `$KRB5CCNAME`) |
| `--aes-key` | Kerberos AES hex key |
| `--pfx`, `--pfx-password` | Client certificate (PKINIT) as a PFX file |
| `--dc` | Domain controller (needed when the KDC/realm cannot be discovered via DNS) |
| `--class-id` | CLSID of the OPC DA server to activate (default: Matrikon simulator `F8582CF2-88FB-11D0-B850-00C0F0104305`) |
| `--named-pipe` | Use named pipe (SMB) transport instead of TCP |
| `--no-seal` | Disable sealing (encryption); downgrade to sign-only |
| `--socks` | SOCKS5 proxy server (also `$SOCKS5_SERVER`) |
| `--timeout` | Operation timeout (default `30s`) |
| `-o, --output` | Output format: `json` (default) or `yaml` |
| `--debug` | Verbose protocol/auth logging on stderr |

Authentication examples (any command):

```sh
# username + password (NTLM)
gopcda status 192.168.56.10 -u administrator -p 'S3cret!'

# NT hash (pass-the-hash)
gopcda status host -u administrator -H aad3b435b51404eeaad3b435b51404ee:5fbc...ae76

# Kerberos with an explicit DC (password turned into a TGT on the fly)
gopcda status dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local

# Kerberos with a pre-existing ccache
kinit user@MSAD.LOCAL && gopcda status dc01.msad.local -k --ccache "$KRB5CCNAME"

# through a SOCKS5 proxy
gopcda status host --socks 127.0.0.1:1080 -u user -p pass
```

The examples below target the bundled **Matrikon OPC Simulation Server** on a
Kerberos demo host, so they share the flags
`-u username -p passw0rd -k --dc dc01.msad.local`.

### `status` - server status

`IOPCServer::GetStatus`. No arguments beyond the target.

```sh
gopcda status dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local
```

```json
{
  "start_time": "2026-09-09T16:11:53.007924756Z",
  "current_time": "2026-09-09T17:07:13.007712345Z",
  "last_update_time": "2026-09-09T17:07:13.003221161Z",
  "server_state": 1,
  "group_count": 0,
  "bandwidth": 4294967295,
  "major_version": 2,
  "minor_version": 0,
  "build_number": 0,
  "vendor_information": "Matrikon Inc +1-780-945-4011 http://www.matrikonopc.com"
}
```

### `browse` - enumerate the address space

`IOPCBrowseServerAddressSpace`. Prints the (fully-qualified) item IDs as a JSON
array.

| Flag | Description |
|---|---|
| `--type` | `flat` (default), `branch`, or `leaf` |
| `--filter` | Server-specific wildcard, e.g. `Random.*` |
| `--position` | Move to this branch before browsing |

```sh
gopcda browse dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local --filter 'Random.*'
```

```json
[
  "Random.ArrayOfReal8",
  "Random.Boolean",
  "Random.Int4",
  "Random.Money",
  "Random.Real8",
  "Random.String",
  "Random.UInt4"
]
```

### `props` - item properties

`IOPCItemProperties`: `QueryAvailableProperties` + `GetItemProperties`. Accepts
one or more item IDs; prints a map of item -> property list (`id`, `description`,
`value`).

```sh
gopcda props dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local Random.Int4
```

```json
{
  "Random.Int4": [
    { "id": 1, "description": "Item Canonical DataType", "value": 3 },
    { "id": 2, "description": "Item Value", "value": 13885 },
    { "id": 3, "description": "Item Quality", "value": 192 },
    { "id": 5, "description": "Item Access Rights", "value": 1 },
    { "id": 6, "description": "Server Scan Rate", "value": 100 },
    { "id": 101, "description": "Item Description", "value": "Random value." }
  ]
}
```

### `read` - read item values

Creates a group (`IOPCServer::AddGroup`), adds the items
(`IOPCItemMgt::AddItems`), and reads them (`IOPCSyncIO::Read`), then removes the
group.

| Flag | Description |
|---|---|
| `--source` | `cache` (default) or `device` |
| `--rate` | Group update rate in milliseconds (default `1000`) |

Quality `192` is `GOOD`. Reading from **cache** right after adding items can
return a `BAD` quality until the first group update - use `--source device` for
an immediate live read.

```sh
gopcda read dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local \
    --source device Random.Int4 Random.Real8 Random.Boolean
```

```json
[
  { "item": "Random.Int4",    "value": 3202,         "quality": 192, "timestamp": "2026-09-09T17:07:22.00992Z" },
  { "item": "Random.Real8",   "value": 8970.2735481, "quality": 192, "timestamp": "2026-09-09T17:07:22.00992Z" },
  { "item": "Random.Boolean", "value": -1,           "quality": 192, "timestamp": "2026-09-09T17:07:22.00992Z" }
]
```

(`VARIANT_BOOL` true is `-1` on the wire.) YAML output (`-o yaml`):

```yaml
- item: Random.Int4
  quality: 192
  timestamp: 2026-09-09T17:10:47.00162Z
  value: 18061
```

### `write` - write item values

Same group/item plumbing, then `IOPCSyncIO::Write`. Arguments are
`ITEM_ID=VALUE` pairs. `--type` controls the VARIANT type
(`auto|int|double|float|short|bool|string`; `auto` infers int, then double, then
bool, then string). Quote items/values that contain spaces or `=`.

```sh
gopcda write dc01.msad.local -u username -p passw0rd -k --dc dc01.msad.local \
    'Bucket Brigade.Real8=3.14' 'Bucket Brigade.Int4=42'
```

```json
[
  { "item": "Bucket Brigade.Real8", "status": "ok" },
  { "item": "Bucket Brigade.Int4",  "status": "ok" }
]
```

A subsequent `read --source device` confirms the round-trip:

```json
[
  { "item": "Bucket Brigade.Real8", "value": 3.14, "quality": 192, "timestamp": "2026-09-09T17:07:29.00795Z" },
  { "item": "Bucket Brigade.Int4",  "value": 42,   "quality": 192, "timestamp": "2026-09-09T17:07:29.00795Z" }
]
```

### Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `remote_activation: ... ERROR_ACCESS_DENIED (0x00000005)` | DCOM **launch/activation** permission not granted to your credentials on the server (configure with `dcomcnfg`); on workgroup hosts also check `LocalAccountTokenFilterPolicy`. |
| `find DC: lookup ... no such host` | Kerberos realm/KDC not discoverable via DNS - pass `--dc <controller>`. |
| `... RPC_X_BAD_STUB_DATA (0x800706f7)` | An interface used without its own `QueryInterface` IPID, or an IDL string param missing `[string]`. |
| `read` from **cache** returns `BAD` quality | Cache is not populated until the first group update; use `--source device` or wait one `--rate` interval. |
| dial to `:135` times out | Endpoint mapper unreachable - firewall, wrong host, or DCOM disabled. |

Add `--debug` to any command for full protocol and authentication logging.

The Matrikon simulator is **DA-only**. The bindings also cover A&E
(`opc/opcae`), HDA (`opc/opchda`), and Security (`opc/opcsec`), but exercising
those requires a server that exposes them (e.g. Integration Objects' free
simulator, the OPC Foundation / Technosoftware sample servers, or KEPServerEX).

## Interfaces

The bindings are generated per specification into the `opc/` directory:

- **OPC Data Access (`opc/opcda`)** - the core real-time data specification:
  `IOPCServer`, `IOPCItemMgt`, `IOPCGroupStateMgt`, `IOPCSyncIO`,
  `IOPCAsyncIO` / `IOPCAsyncIO2`, `IOPCBrowseServerAddressSpace`,
  `IOPCItemProperties`, `IOPCPublicGroupStateMgt`,
  `IOPCServerPublicGroups`, `IEnumOPCItemAttributes`, and the client callback
  `IOPCDataCallback`.
- **OPC Historical Data Access (`opc/opchda`)** - access to archived (historical)
  process data rather than just current values. `IOPCHDA_Server` is the root
  interface (item handles, item attributes, aggregates); `IOPCHDA_Browser`
  browses the historian address space; `IOPCHDA_SyncRead` / `IOPCHDA_AsyncRead`
  read raw, processed, modified, or at-time historical values;
  `IOPCHDA_SyncUpdate` / `IOPCHDA_AsyncUpdate` insert, replace, and delete
  archived values; `IOPCHDA_SyncAnnotations` / `IOPCHDA_AsyncAnnotations` read
  and write annotations attached to the data; `IOPCHDA_Playback` streams
  historical data over time; `IOPCHDA_DataCallback` delivers asynchronous
  results to the client.
- **OPC Security (`opc/opcsec`)** - standardizes how OPC clients authenticate to
  a server and how the server reports available access rights. `IOPCSecurityNT`
  reports whether the server relies on Windows NT security and what permissions
  are available; `IOPCSecurityPrivate` lets a client log on and off with a
  private (server-specific username/password) credential when NT security is not
  used.
- **OPC Common (`opc/opccomn`)** - interfaces shared by every COM-based OPC
  server type (DA, HDA, A&E): `IOPCCommon` (LocaleID handling and error-string
  lookup), `IOPCServerList`, and the client-implemented shutdown callback
  `IOPCShutdown`.
- **OPC Server Enumerator (`opc/opcenum`)** - Go bindings for OpcEnum, the
  well-known local service that lets clients discover which OPC servers are
  registered on a machine by component category, without reading the registry
  directly. Exposes `IOpcServerList` and `IOpcServerList2` (enumerate server
  CLSIDs by category, resolve between ProgID and CLSID).
- **COM SDK base (`opc/sdk`)** - the COM plumbing the OPC interfaces depend on:
  `IUnknown`, `IClassFactory` and related base types, plus the component
  category manager `ICatInformation` (comcat).

An OPC Alarms and Events definition (`idl/opcae.idl`, `IOPCEventServer` and
friends) is also present in the tree.

The IDL sources live under `idl/` (`opcda.idl`, `opchda.idl`, `opcsec.idl`,
`opcenum.idl`, `opccomn.idl`, `opcae.idl`, and `idl/sdk/*.idl`).
The generated Go code under `opc/` is committed to the repository; regenerate it
with the Makefile targets below.

### IDL provenance

The OPC IDL headers in this tree were assembled from publicly available copies
of the OPC Foundation definitions, in particular:

- OPCF-Members/UA-.NET-Legacy, `SampleApplications/Include`:
  https://github.com/OPCF-Members/UA-.NET-Legacy/tree/master/SampleApplications/Include
- mtconnect/adapter, `opc`:
  https://github.com/mtconnect/adapter/tree/master/opc

The canonical specifications remain those published by the OPC Foundation
(https://opcfoundation.org).

## Prerequisites

- Docker (the code generator runs as a container image).
- The go-msrpc IDL definitions available at `../go-msrpc/idl`, i.e. a checkout
  of oiweiwei/go-msrpc as a sibling directory of this repository. Only the
  `idl/` folder is needed for code generation; a sparse checkout is enough:

  ```sh
  git clone --no-checkout --filter=blob:none \
      https://github.com/oiweiwei/go-msrpc.git ../go-msrpc
  git -C ../go-msrpc sparse-checkout set idl
  git -C ../go-msrpc checkout
  ```

## Makefile targets

- **`make all`** - regenerate every package: `opccomn.go`, `opcda.go`,
  `opchda.go`, `opcsec.go`, `opcenum.go`, `sdk/unknwn.go`, `sdk/comcat.go` and
  `sdk/objidlbase.go`. This is the target used to keep the committed code in
  `opc/` in sync with the IDL.
- **`make <name>.go`** - pattern target that regenerates a single package from
  its matching `<name>.idl` (for example `make opcda.go` or
  `make sdk/comcat.go`).
- **`make test`** - run the Go test suite (`go test ./opc/...`).

### Variables

- **`DOCKER_IMAGE`** - the code generator image (default
  `ghcr.io/oiweiwei/midl-gen-go`).
- **`DEBUG=1`** - pass `--verbose` to the generator for extra diagnostics.

## Regenerating and checking in

After changing any IDL, run `make all` and commit the resulting changes under
`opc/`. Continuous integration runs `make all` on every push and fails if the
committed generated code does not match a fresh generation (see
`.github/workflows/generate.yml`).

## License

See the upstream projects for their respective licenses. go-msrpc is
distributed under the MIT License.
