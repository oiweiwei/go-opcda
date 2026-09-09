// Command gopcda is a small OPC Classic Data Access (OPC DA) client that talks
// to a remote OPC server over DCOM / MS-RPC, without depending on Windows COM.
//
// It exercises the operations a real OPC client (SCADA/HMI/historian
// front-end) performs against a classic OPC DA server:
//
//	status  - activate the server and read IOPCServer::GetStatus
//	browse  - walk the server address space (IOPCBrowseServerAddressSpace)
//	props   - read item properties (IOPCItemProperties)
//	read    - add a group + items and IOPCSyncIO::Read them (cache or device)
//	write   - add a group + items and IOPCSyncIO::Write values to them
//
// Authentication and targeting are provided by RedTeamPentesting/adauth, so it
// supports username/password, NT hashes, Kerberos (ccache / AES key), PFX
// certificates, and SOCKS5 proxying - the same flags as the wmiq tool.
//
//	gopcda [flags] status <target>
//	gopcda [flags] browse <target> --filter 'Random.*'
//	gopcda [flags] read   <target> --source device Random.Int4 Random.Real8
//	gopcda [flags] write  <target> 'Bucket Brigade.Real8=3.14'
//	gopcda [flags] props  <target> Random.Int4
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/RedTeamPentesting/adauth"
	"github.com/RedTeamPentesting/adauth/dcerpcauth"

	"github.com/oiweiwei/go-msrpc/dcerpc"
	"github.com/oiweiwei/go-msrpc/midl/uuid"

	"github.com/oiweiwei/go-msrpc/msrpc/dcetypes"
	"github.com/oiweiwei/go-msrpc/msrpc/well_known"

	"github.com/oiweiwei/go-msrpc/msrpc/dcom"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom/iactivation/v0"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom/iobjectexporter/v0"
	iremunknown2 "github.com/oiweiwei/go-msrpc/msrpc/dcom/iremunknown2/v0"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom/oaut"
	ienumstring "github.com/oiweiwei/go-msrpc/msrpc/dcom/urlmon/ienumstring/v0"
	"github.com/oiweiwei/go-msrpc/msrpc/dtyp"

	"github.com/oiweiwei/go-msrpc/msrpc/erref/hresult"
	_ "github.com/oiweiwei/go-msrpc/msrpc/erref/ntstatus"
	_ "github.com/oiweiwei/go-msrpc/msrpc/erref/win32"

	"github.com/oiweiwei/go-opcda/opc/opcda"
	opcda_client "github.com/oiweiwei/go-opcda/opc/opcda/client"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcbrowseserveraddressspace/v0"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcitemmgt/v0"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcitemproperties/v0"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcserver/v0"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcsyncio/v0"
)

// Matrikon OPC Simulation Server CLSID, used as the default class ID.
const opcSimulatorCLSID = "F8582CF2-88FB-11D0-B850-00C0F0104305"

var (
	// authentication / targeting (adauth), shared by every subcommand.
	debug          bool
	socksServer    = os.Getenv("SOCKS5_SERVER")
	authOpts       = &adauth.Options{}
	dcerpcauthOpts = &dcerpcauth.Options{}

	// transport / activation options.
	namedPipe    bool
	noSeal       bool
	timeout      time.Duration
	classID      string
	outputFormat string

	logger = zerolog.New(io.Discard)
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gopcda",
		Short:         "OPC Classic Data Access (OPC DA) client over DCOM",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// wire up debug logging once flags are parsed.
			authOpts.Debug = adauth.NewDebugFunc(&debug, os.Stderr, true)
			dcerpcauthOpts.Debug = authOpts.Debug
			if debug {
				logger = zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
					w.Out = os.Stderr
					w.TimeFormat = time.DateTime
				})).With().Timestamp().Logger()
				authOpts.Debug = logger.Printf
				dcerpcauthOpts.Debug = logger.Printf
			}
			dcerpcauthOpts.KerberosDialer = adauth.DialerWithSOCKS5ProxyIfSet(socksServer, nil)
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.BoolVar(&debug, "debug", false, "Enable debug output")
	pf.StringVar(&socksServer, "socks", socksServer, "SOCKS5 proxy server")
	pf.BoolVar(&namedPipe, "named-pipe", false, "Use named pipe (SMB) as transport")
	pf.BoolVar(&noSeal, "no-seal", false, "Disable sealing (encryption) of DCERPC messages")
	pf.DurationVar(&timeout, "timeout", 30*time.Second, "Timeout for the operation")
	pf.StringVar(&classID, "class-id", opcSimulatorCLSID, "CLSID of the OPC DA server to activate")
	pf.StringVarP(&outputFormat, "output", "o", "json", "Output format (json, yaml)")

	// adauth registers -u/-p/-k/-H/--ccache/--aes-key/--pfx/--dc/... .
	authOpts.RegisterFlags(pf)

	root.AddCommand(statusCmd(), browseCmd(), propsCmd(), readCmd(), writeCmd(), aeCmd())
	return root
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <target>",
		Short: "Activate the server and print IOPCServer::GetStatus",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := connect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			st, err := s.server(ctx).GetStatus(ctx, &iopcserver.GetStatusRequest{This: s.this})
			if err != nil {
				return fmt.Errorf("get_status: %w", err)
			}
			return encode(st.ServerStatus)
		},
	}
}

func browseCmd() *cobra.Command {
	var (
		typ    string
		filter string
		pos    string
	)
	cmd := &cobra.Command{
		Use:   "browse <target>",
		Short: "Browse the server address space and list item IDs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := connect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			browseType := opcda.BrowseTypeFlat
			switch strings.ToLower(typ) {
			case "flat":
				browseType = opcda.BrowseTypeFlat
			case "branch":
				browseType = opcda.BrowseTypeBranch
			case "leaf":
				browseType = opcda.BrowseTypeLeaf
			default:
				return fmt.Errorf("unknown browse type %q (use flat|branch|leaf)", typ)
			}

			browseIPID, err := s.queryInterface(ctx, s.serverIPID, iopcbrowseserveraddressspace.BrowseServerAddressSpaceIID)
			if err != nil {
				return err
			}
			browse := s.iface(ctx, browseIPID).BrowseServerAddressSpace()

			// optionally move the browse cursor to a branch first.
			if pos != "" {
				if _, err := browse.ChangeBrowsePosition(ctx, &iopcbrowseserveraddressspace.ChangeBrowsePositionRequest{
					This:            s.this,
					BrowseDirection: opcda.BrowseDirectionTo,
					String:          pos,
				}); err != nil {
					return fmt.Errorf("change_browse_position: %w", err)
				}
			}

			res, err := browse.BrowseOPCItemIDs(ctx, &iopcbrowseserveraddressspace.BrowseOPCItemIDsRequest{
				This:             s.this,
				BrowseFilterType: browseType,
				FilterCriteria:   filter,
			})
			if err != nil {
				return fmt.Errorf("browse_opc_item_ids: %w", err)
			}
			if res.IEnumString == nil {
				return nil
			}

			names, err := s.enumStrings(ctx, res.IEnumString.InterfacePointer().IPID())
			if err != nil {
				return err
			}

			ids := make([]string, 0, len(names))
			for _, n := range names {
				// resolve a browse name to its fully-qualified item ID.
				id, err := browse.GetItemID(ctx, &iopcbrowseserveraddressspace.GetItemIDRequest{This: s.this, ItemDataID: n})
				if err == nil && id.ItemID != "" {
					ids = append(ids, id.ItemID)
				} else {
					ids = append(ids, n)
				}
			}
			return encode(ids)
		},
	}
	cmd.Flags().StringVar(&typ, "type", "flat", "browse type: flat|branch|leaf")
	cmd.Flags().StringVar(&filter, "filter", "", "filter criteria (server-specific wildcard, e.g. Random.*)")
	cmd.Flags().StringVar(&pos, "position", "", "move to this branch before browsing")
	return cmd
}

func propsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "props <target> ITEM_ID...",
		Short: "Query available properties and their values for the given items",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := connect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			propsIPID, err := s.queryInterface(ctx, s.serverIPID, iopcitemproperties.ItemPropertiesIID)
			if err != nil {
				return err
			}
			props := s.iface(ctx, propsIPID).ItemProperties()
			out := map[string]any{}

			for _, item := range args[1:] {
				avail, err := props.QueryAvailableProperties(ctx, &iopcitemproperties.QueryAvailablePropertiesRequest{
					This:   s.this,
					ItemID: item,
				})
				// S_FALSE (mapped to ErrorArithmeticOverflow) means partial success:
				// the array is returned but some entries carry per-item errors.
				if err != nil && !errors.Is(err, hresult.ErrorArithmeticOverflow) {
					return fmt.Errorf("query_available_properties %q: %w", item, err)
				}

				vals, err := props.GetItemProperties(ctx, &iopcitemproperties.GetItemPropertiesRequest{
					This:        s.this,
					ItemID:      item,
					Count:       avail.Count,
					PropertyIDs: avail.PropertyIDs,
				})
				if err != nil && !errors.Is(err, hresult.ErrorArithmeticOverflow) {
					return fmt.Errorf("get_item_properties %q: %w", item, err)
				}

				itemProps := []map[string]any{}
				for i := range avail.PropertyIDs {
					p := map[string]any{
						"id":          avail.PropertyIDs[i],
						"description": avail.Descriptions[i],
					}
					if i < len(vals.Data) {
						p["value"] = variantValue(vals.Data[i])
					}
					if i < len(vals.Errors) && vals.Errors[i] != 0 {
						p["error"] = hresult.FromCode(uint32(vals.Errors[i])).Error()
					}
					itemProps = append(itemProps, p)
				}
				out[item] = itemProps
			}
			return encode(out)
		},
	}
}

func readCmd() *cobra.Command {
	var (
		source string
		rate   uint32
	)
	cmd := &cobra.Command{
		Use:   "read <target> ITEM_ID...",
		Short: "Add a group + items and synchronously read their values",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := connect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			items := args[1:]

			ds := opcda.DataSourceCache
			if strings.EqualFold(source, "device") {
				ds = opcda.DataSourceDevice
			}

			g, err := s.addGroup(ctx, "gopcda-read", rate)
			if err != nil {
				return err
			}
			defer g.Close(ctx)

			handles, errs, err := g.addItems(ctx, items, 0)
			if err != nil {
				return err
			}

			read, err := g.sync.Read(ctx, &iopcsyncio.ReadRequest{
				This:   s.this,
				Source: ds,
				Count:  uint32(len(handles)),
				Server: handles,
			})
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}

			out := []map[string]any{}
			for i, item := range items {
				row := map[string]any{"item": item}
				if errs[i] != 0 {
					row["add_error"] = hresult.FromCode(uint32(errs[i])).Error()
					out = append(out, row)
					continue
				}
				if i < len(read.ItemValues) && read.ItemValues[i] != nil {
					st := read.ItemValues[i]
					row["value"] = variantValue(st.DataValue)
					row["quality"] = st.Quality
					if st.Timestamp != nil {
						row["timestamp"] = st.Timestamp.AsTime()
					}
				}
				if i < len(read.Errors) && read.Errors[i] != 0 {
					row["read_error"] = hresult.FromCode(uint32(read.Errors[i])).Error()
				}
				out = append(out, row)
			}
			return encode(out)
		},
	}
	cmd.Flags().StringVar(&source, "source", "cache", "read source: cache|device")
	cmd.Flags().Uint32Var(&rate, "rate", 1000, "group update rate in milliseconds")
	return cmd
}

func writeCmd() *cobra.Command {
	var typ string
	cmd := &cobra.Command{
		Use:   "write <target> ITEM_ID=VALUE...",
		Short: "Add a group + items and synchronously write values",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			items := make([]string, 0, len(args)-1)
			values := make([]*oaut.Variant, 0, len(args)-1)
			for _, a := range args[1:] {
				name, raw, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("expected ITEM_ID=VALUE, got %q", a)
				}
				v, err := parseVariant(typ, raw)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				items = append(items, name)
				values = append(values, v)
			}

			s, err := connect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			g, err := s.addGroup(ctx, "gopcda-write", 1000)
			if err != nil {
				return err
			}
			defer g.Close(ctx)

			handles, addErrs, err := g.addItems(ctx, items, 0)
			if err != nil {
				return err
			}

			w, err := g.sync.Write(ctx, &iopcsyncio.WriteRequest{
				This:       s.this,
				Count:      uint32(len(handles)),
				Server:     handles,
				ItemValues: values,
			})
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}

			out := []map[string]any{}
			for i, item := range items {
				row := map[string]any{"item": item}
				switch {
				case addErrs[i] != 0:
					row["add_error"] = hresult.FromCode(uint32(addErrs[i])).Error()
				case i < len(w.Errors) && w.Errors[i] != 0:
					row["write_error"] = hresult.FromCode(uint32(w.Errors[i])).Error()
				default:
					row["status"] = "ok"
				}
				out = append(out, row)
			}
			return encode(out)
		},
	}
	cmd.Flags().StringVar(&typ, "type", "auto", "value type: auto|int|double|float|short|bool|string")
	return cmd
}

// ---------------------------------------------------------------------------
// session: DCOM activation + connection (adauth-authenticated)
// ---------------------------------------------------------------------------

// activation is a live DCOM object: the SCM connection, the OXID connection to
// the activated object, and the bookkeeping (ORPCThis, the activated
// interface's IPID, and the object exporter's Remote Unknown) needed to call
// and QueryInterface further interfaces on it.
//
// It is protocol-agnostic: OPC DA, OPC A&E and OPCENUM all activate a CLSID the
// same way and differ only in the client set bound on top of conn. The caller
// binds its client set and assigns ru = client.RemoteUnknown2() so that
// queryInterface works.
type activation struct {
	scm        dcerpc.Conn   // well-known-endpoint connection
	conn       dcerpc.Conn   // OXID connection to the activated object
	this       *dcom.ORPCThis
	serverIPID *dcom.IPID    // interface pointer of the activated object (the requested IID)
	remUnknown *dcom.IPID    // ipidRemUnknown of the object exporter (for QueryInterface)
	authOpt    dcerpc.Option // per-client seal/sign option
	ru         iremunknown2.RemoteUnknown2Client
}

func (a *activation) Close(ctx context.Context) {
	if a.conn != nil {
		a.conn.Close(ctx) //nolint:errcheck
	}
	if a.scm != nil {
		a.scm.Close(ctx) //nolint:errcheck
	}
}

// activate resolves credentials for target via adauth, performs the DCOM remote
// activation of clsID for interface iid, and dials the resulting object. Every
// field of the returned activation is set except ru, which the caller fills in
// from its bound client set (client.RemoteUnknown2()).
func activate(ctx context.Context, targetSpec string, clsID *uuid.UUID, iid *dcom.IID) (*activation, error) {
	creds, target, err := authOpts.WithTarget(ctx, "host", targetSpec)
	if err != nil {
		return nil, err
	}

	dcerpcOpts, err := dcerpcauth.AuthenticationOptions(ctx, creds, target, dcerpcauthOpts)
	if err != nil {
		return nil, err
	}
	dcerpcOpts = append(dcerpcOpts,
		dcerpc.WithDialer(adauth.DialerWithSOCKS5ProxyIfSet(socksServer, nil)),
		dcerpc.WithTimeout(timeout),
		dcerpc.WithLogger(logger),
		well_known.EndpointMapper(),
	)

	// seal (encrypt) by default; --no-seal downgrades to sign-only.
	authOpt := dcerpc.WithSeal()
	if noSeal {
		authOpt = dcerpc.WithSign()
	}

	proto := "ncacn_ip_tcp"
	if namedPipe {
		proto = "ncacn_np"
	}

	// ObjectExporter lives on the well-known endpoint (135 / SMB).
	scm, err := dcerpc.Dial(ctx, target.Address(), dcerpcOpts...)
	if err != nil {
		return nil, fmt.Errorf("dial_well_known_endpoint: %w", err)
	}

	oxe, err := iobjectexporter.NewObjectExporterClient(ctx, scm, authOpt)
	if err != nil {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("new_object_exporter: %w", err)
	}

	srv, err := oxe.ServerAlive2(ctx, &iobjectexporter.ServerAlive2Request{})
	if err != nil {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("server_alive2: %w", err)
	}

	act, err := iactivation.NewActivationClient(ctx, scm, authOpt)
	if err != nil {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("new_activation_client: %w", err)
	}

	res, err := act.RemoteActivation(ctx, &iactivation.RemoteActivationRequest{
		ORPCThis:                   &dcom.ORPCThis{Version: srv.COMVersion},
		ClassID:                    dtyp.GUIDFromUUID(clsID),
		IIDs:                       []*dcom.IID{iid},
		RequestedProtocolSequences: []uint16{uint16(dcetypes.ProtocolTCP), uint16(dcetypes.ProtocolNamedPipe)},
	})
	if err != nil {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("remote_activation: %w", err)
	}
	if res.HResult != 0 {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("remote_activation: %s", hresult.FromCode(uint32(res.HResult)))
	}

	for _, binding := range res.OXIDBindings.GetStringBindings() {
		dcerpcauthOpts.Debug("found binding: %s", binding)
	}

	// dial the activated object using the OXID bindings from the SCM.
	conn, err := dcerpc.Dial(ctx, target.Address(), append(dcerpcOpts, res.OXIDBindings.EndpointsByProtocol(proto)...)...)
	if err != nil {
		scm.Close(ctx) //nolint:errcheck
		return nil, fmt.Errorf("dial_oxid_endpoint: %w", err)
	}

	return &activation{
		scm:        scm,
		conn:       conn,
		this:       &dcom.ORPCThis{Version: srv.COMVersion},
		serverIPID: res.InterfaceData[0].IPID(),
		remUnknown: res.RemoteUnknown,
		authOpt:    authOpt,
	}, nil
}

// session is an activated OPC DA server (IOPCServer).
type session struct {
	*activation
	opc opcda_client.Client
}

// connect performs the DCOM remote activation of the OPC DA server CLSID for
// the IOPCServer interface and returns a session bound to that object.
func connect(ctx context.Context, targetSpec string) (*session, error) {
	clsID, err := uuid.Parse(classID)
	if err != nil {
		return nil, fmt.Errorf("parse class-id: %w", err)
	}

	a, err := activate(ctx, targetSpec, clsID, iopcserver.ServerIID)
	if err != nil {
		return nil, err
	}

	opc, err := opcda_client.NewClient(ctx, a.conn, a.authOpt)
	if err != nil {
		a.Close(ctx)
		return nil, fmt.Errorf("new_opcda_client: %w", err)
	}
	a.ru = opc.RemoteUnknown2()

	return &session{activation: a, opc: opc}, nil
}

// iface returns a copy of the opcda client set whose calls target the given
// interface pointer (IPID) on the activated object.
func (s *session) iface(ctx context.Context, ipid *dcom.IPID) opcda_client.Client {
	return s.opc.IPID(ctx, ipid)
}

// server returns the IOPCServer client bound to the activated object.
func (s *session) server(ctx context.Context) iopcserver.ServerClient {
	return s.iface(ctx, s.serverIPID).Server()
}

// enumStrings drains an IEnumString enumerator (returned by BrowseOPCItemIDs
// and by the A&E area browser).
func (a *activation) enumStrings(ctx context.Context, ipid *dcom.IPID) ([]string, error) {
	es, err := ienumstring.NewEnumStringClient(ctx, a.conn, a.authOpt)
	if err != nil {
		return nil, fmt.Errorf("new_enum_string: %w", err)
	}
	es = es.IPID(ctx, ipid)

	const batch = 128
	var names []string
	for {
		next, err := es.Next(ctx, &ienumstring.NextRequest{This: a.this, Count: batch})
		// S_FALSE (mapped to ErrorArithmeticOverflow) means the enumerator
		// returned fewer than requested - the last, partial batch.
		if err != nil && !errors.Is(err, hresult.ErrorArithmeticOverflow) {
			return nil, fmt.Errorf("enum_string_next: %w", err)
		}
		if next == nil {
			break
		}
		names = append(names, next.Entries...)
		if next.Fetched < batch {
			break
		}
	}
	return names, nil
}

// queryInterface performs a DCOM QueryInterface: it asks the object exporter's
// Remote Unknown (reached via the well-known ipidRemUnknown returned by
// activation) for the interface iid on the object identified by objectIPID, and
// returns the IPID of the resulting interface pointer.
//
// This is required because every COM interface on an object has its own IPID:
// activation only handed us IOPCServer, so IOPCBrowseServerAddressSpace,
// IOPCItemProperties, IOPCSyncIO, ... must each be resolved this way.
func (a *activation) queryInterface(ctx context.Context, objectIPID *dcom.IPID, iid *dcom.IID) (*dcom.IPID, error) {
	res, err := a.ru.RemoteQueryInterface2(ctx, &iremunknown2.RemoteQueryInterface2Request{
		This:      a.this,
		IPID:      objectIPID.GUID(),
		IIDsCount: 1,
		IIDs:      []*dcom.IID{iid},
	}, dcom.WithIPID(a.remUnknown))
	if err != nil {
		return nil, fmt.Errorf("query_interface: %w", err)
	}
	if len(res.HResult) > 0 && res.HResult[0] != 0 {
		return nil, fmt.Errorf("query_interface: %s", hresult.FromCode(uint32(res.HResult[0])))
	}
	if len(res.Interface) == 0 || res.Interface[0] == nil {
		return nil, fmt.Errorf("query_interface: no interface returned")
	}
	return res.Interface[0].IPID(), nil
}

// ---------------------------------------------------------------------------
// group: a live OPC group bound to the activated server
// ---------------------------------------------------------------------------

type group struct {
	s      *session
	handle uint32              // server group handle (for RemoveGroup)
	opc    opcda_client.Client // client bound to the group object's IPID
	items  iopcitemmgt.ItemManagementClient
	sync   iopcsyncio.SyncIOClient
}

// addGroup creates an OPC group on the server. The group is a distinct DCOM
// object; we query it for the IOPCItemMgt and IOPCSyncIO interfaces and bind a
// client to each (in classic COM every interface has its own IPID, so we must
// QueryInterface rather than reuse the group's returned pointer for all of
// them).
func (s *session) addGroup(ctx context.Context, name string, rate uint32) (*group, error) {
	add, err := s.server(ctx).AddGroup(ctx, &iopcserver.AddGroupRequest{
		This:                s.this,
		Name:                name,
		Active:              true,
		RequestedUpdateRate: rate,
		ClientGroup:         1,
		RIID:                iopcitemmgt.ItemManagementIID,
	})
	if err != nil {
		return nil, fmt.Errorf("add_group: %w", err)
	}
	if add.Return != 0 {
		return nil, fmt.Errorf("add_group: %s", hresult.FromCode(uint32(add.Return)))
	}
	if add.Unknown == nil {
		return nil, fmt.Errorf("add_group: server returned no interface pointer")
	}

	g := &group{s: s, handle: add.ServerGroup}

	// the pointer returned by AddGroup is IOPCItemMgt (riid above).
	itemIPID := add.Unknown.InterfacePointer().IPID()
	g.opc = s.opc.IPID(ctx, itemIPID)
	g.items = g.opc.ItemManagement()

	// resolve IOPCSyncIO on the same group object.
	syncIPID, err := s.queryInterface(ctx, itemIPID, iopcsyncio.SyncIOIID)
	if err != nil {
		return nil, err
	}
	g.sync = g.opc.IPID(ctx, syncIPID).SyncIO()

	return g, nil
}

// addItems adds the given item IDs to the group and returns the resulting
// server handles (one per input, zero where the add failed) plus the per-item
// add errors.
func (g *group) addItems(ctx context.Context, itemIDs []string, dataType uint16) (handles []uint32, errs []int32, err error) {
	defs := make([]*opcda.ItemDefinition, len(itemIDs))
	for i, id := range itemIDs {
		defs[i] = &opcda.ItemDefinition{
			ItemID:            id,
			Active:            true,
			Client:            uint32(i + 1),
			RequestedDataType: dataType,
		}
	}

	res, err := g.items.AddItems(ctx, &iopcitemmgt.AddItemsRequest{
		This:      g.s.this,
		Count:     uint32(len(defs)),
		ItemArray: defs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("add_items: %w", err)
	}

	handles = make([]uint32, len(itemIDs))
	errs = make([]int32, len(itemIDs))
	for i := range itemIDs {
		if i < len(res.Errors) {
			errs[i] = res.Errors[i]
		}
		if i < len(res.AddResults) && res.AddResults[i] != nil {
			handles[i] = res.AddResults[i].Server
		}
	}
	return handles, errs, nil
}

// Close removes the group from the server.
func (g *group) Close(ctx context.Context) {
	if g.handle == 0 {
		return
	}
	if _, err := g.s.server(ctx).RemoveGroup(ctx, &iopcserver.RemoveGroupRequest{
		This:        g.s.this,
		ServerGroup: g.handle,
		Force:       true,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "remove_group:", err)
	}
}

// ---------------------------------------------------------------------------
// variant helpers
// ---------------------------------------------------------------------------

// variantValue extracts a Go-friendly value from an OLE VARIANT.
func variantValue(v *oaut.Variant) any {
	if v == nil || v.VarUnion == nil {
		return nil
	}
	val := v.VarUnion.GetValue()
	if s, ok := val.(*oaut.String); ok {
		if s == nil {
			return nil
		}
		return s.Data
	}
	return val
}

// parseVariant builds an OLE VARIANT from a string value and a type hint.
func parseVariant(typ, raw string) (*oaut.Variant, error) {
	switch strings.ToLower(typ) {
	case "auto":
		if i, err := strconv.ParseInt(raw, 10, 32); err == nil {
			return longVariant(int32(i)), nil
		}
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return doubleVariant(f), nil
		}
		if b, err := strconv.ParseBool(raw); err == nil {
			return boolVariant(b), nil
		}
		return stringVariant(raw), nil
	case "int", "i4":
		i, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return longVariant(int32(i)), nil
	case "short", "i2":
		i, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			return nil, err
		}
		return newVariant(oaut.VarEnumI2, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_Short{Short: int16(i)}}), nil
	case "double", "r8":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		return doubleVariant(f), nil
	case "float", "r4":
		f, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return nil, err
		}
		return newVariant(oaut.VarEnumR4, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_Float{Float: float32(f)}}), nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, err
		}
		return boolVariant(b), nil
	case "string", "bstr":
		return stringVariant(raw), nil
	default:
		return nil, fmt.Errorf("unknown type %q", typ)
	}
}

func newVariant(vt oaut.VarEnum, un *oaut.Variant_VarUnion) *oaut.Variant {
	return &oaut.Variant{VT: uint16(vt), VarUnion: un}
}

func longVariant(v int32) *oaut.Variant {
	return newVariant(oaut.VarEnumI4, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_Long{Long: v}})
}

func doubleVariant(v float64) *oaut.Variant {
	return newVariant(oaut.VarEnumR8, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_Double{Double: v}})
}

func boolVariant(v bool) *oaut.Variant {
	return newVariant(oaut.VarEnumBool, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_Bool{Bool: boolVal(v)}})
}

func stringVariant(v string) *oaut.Variant {
	return newVariant(oaut.VarEnumString, &oaut.Variant_VarUnion{Value: &oaut.Variant_VarUnion_BSTR{BSTR: &oaut.String{Data: v}}})
}

// boolVal returns the VARIANT_BOOL wire value (-1 for true, 0 for false).
func boolVal(b bool) int16 {
	if b {
		return -1
	}
	return 0
}

// ---------------------------------------------------------------------------
// output
// ---------------------------------------------------------------------------

// encode writes v to stdout in the selected output format (json or yaml).
func encode(v any) error {
	switch strings.ToLower(outputFormat) {
	case "yaml", "yml":
		enc := yaml.NewEncoder(os.Stdout)
		defer enc.Close()
		return enc.Encode(v)
	default:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}
