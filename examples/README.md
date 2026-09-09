# go-opcda examples

Small, self-contained programs that talk to a **classic OPC Data Access (OPC DA)**
server over DCOM / MS-RPC using the [`go-opcda`](../) bindings.

> Looking for the full command-line client? It now lives at
> [`cmd/gopcda`](../cmd/gopcda) and is documented in the
> [top-level README](../README.md#command-line-client-gopcda).

| Example | What it shows |
|---|---|
| [`activate-simul`](./activate-simul) | The minimal path: DCOM-activate an OPC DA server by CLSID and call `IOPCServer::GetStatus`. Start here. |

## `activate-simul`

The smallest possible go-opcda program - one activation, one status call. It
uses go-msrpc's `config` connection-string flags directly (the `gopcda` CLI uses
`adauth` instead).

```sh
# positional connection string; --class-id defaults to the Matrikon simulator
go run ./examples/activate-simul 'user%pass@ncacn_ip_tcp:host[krb5,privacy]'

# activate a different OPC DA server by CLSID
go run ./examples/activate-simul \
    --class-id=F8582CF2-88FB-11D0-B850-00C0F0104305 \
    'user%pass@ncacn_ip_tcp:host[krb5,privacy]'
```

It prints the decoded `OPCSERVERSTATUS` (server state, vendor info, versions,
group count, ...) as JSON.

The default CLSID `{F8582CF2-88FB-11D0-B850-00C0F0104305}` is the well-known
**Matrikon OPC Simulation Server**.

### The one non-obvious DCOM detail

Activation only ever hands you the **`IOPCServer`** interface pointer. Every
*other* COM interface on the object (`IOPCBrowseServerAddressSpace`,
`IOPCItemProperties`, `IOPCSyncIO`, `IOPCItemMgt`, ...) has its **own IPID** and
must be obtained with a **`QueryInterface`** - otherwise the server faults with
`RPC_X_BAD_STUB_DATA (0x800706f7)`. See `cmd/gopcda`'s `queryInterface` helper
for how this is done over the wire.
