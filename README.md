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
