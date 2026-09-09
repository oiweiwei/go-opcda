// Command activate-simul remotely activates the OPC Simulator (OPC DA) server
// via DCOM and returns a ready-to-use IOPCServer interface, then prints the
// server status.
//
// It is the smallest possible go-opcda example: one CLSID, one activation, one
// IOPCServer::GetStatus call.
//
// The default class ID is the well-known CLSID of the Matrikon OPC Simulation
// Server:
//
//	{F8582CF2-88FB-11D0-B850-00C0F0104305}
//
// Usage:
//
//	go run ./examples/activate-simul 'username%password@ncacn_ip_tcp:host[privacy]'
//	go run ./examples/activate-simul --class-id=F8582CF2-88FB-11D0-B850-00C0F0104305 'user%pass@ncacn_ip_tcp:host[krb5,privacy]'
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/oiweiwei/go-msrpc/dcerpc"
	"github.com/oiweiwei/go-msrpc/midl/uuid"

	"github.com/oiweiwei/go-msrpc/ssp/gssapi"

	config "github.com/oiweiwei/go-msrpc/config"
	config_flag "github.com/oiweiwei/go-msrpc/config/flag"

	"flag"

	"github.com/oiweiwei/go-msrpc/msrpc/dcom"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom/iactivation/v0"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom/iobjectexporter/v0"
	"github.com/oiweiwei/go-msrpc/msrpc/dtyp"

	"github.com/oiweiwei/go-msrpc/msrpc/erref/hresult"
	_ "github.com/oiweiwei/go-msrpc/msrpc/erref/hresult"
	_ "github.com/oiweiwei/go-msrpc/msrpc/erref/ntstatus"
	_ "github.com/oiweiwei/go-msrpc/msrpc/erref/win32"

	. "github.com/oiweiwei/go-msrpc/examples/common"

	opcda_client "github.com/oiweiwei/go-opcda/opc/opcda/client"
	"github.com/oiweiwei/go-opcda/opc/opcda/iopcserver/v0"
)

var (
	cfg   = config.New()
	clsid string
)

// Matrikon OPC Simulation Server CLSID, used as the default class ID.
const opcSimulatorCLSID = "F8582CF2-88FB-11D0-B850-00C0F0104305"

func init() {
	config_flag.BindFlags(cfg, flag.CommandLine)
	flag.StringVar(&clsid, "class-id", opcSimulatorCLSID, "CLSID of the OPC DA server to activate")
}

func main() {

	if err := config_flag.ParseAndValidate(cfg, flag.CommandLine); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	ctx := gssapi.NewSecurityContext(context.Background())

	// activate the OPC DA server and obtain a bound opcda client set.
	cc, opc, this, err := activate(ctx, clsid)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer cc.Close(ctx)

	// IOPCServer is the root OPC DA interface. Query the server status to prove
	// the activation succeeded.
	status, err := opc.Server().GetStatus(ctx, &iopcserver.GetStatusRequest{This: this})
	if err != nil {
		fmt.Fprintln(os.Stderr, "get_status", err)
		return
	}

	fmt.Println(J(status.ServerStatus))
}

// activate performs the DCOM remote activation of the given CLSID for the
// IOPCServer interface and returns the connection together with a bound opcda
// client set (which exposes IOPCServer, IOPCItemMgt, IOPCSyncIO, ...).
func activate(ctx context.Context, clsid string) (dcerpc.Conn, opcda_client.Client, *dcom.ORPCThis, error) {

	classID, err := uuid.Parse(clsid)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse clsid: %w", err)
	}

	// ObjectExporter uses the well-known endpoint 135.
	cc, err := dcerpc.Dial(ctx, cfg.ServerAddr(), cfg.DialOptions(ctx)...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial_well_known_endpoint: %w", err)
	}

	// new object exporter client.
	cli, err := iobjectexporter.NewObjectExporterClient(ctx, cc, cfg.ClientOptions(ctx)...)
	if err != nil {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("new_object_exporter: %w", err)
	}

	// server-alive to determine the bindings and COM version.
	srv, err := cli.ServerAlive2(ctx, &iobjectexporter.ServerAlive2Request{})
	if err != nil {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("server_alive2: %w", err)
	}

	// new activation client.
	iact, err := iactivation.NewActivationClient(ctx, cc, cfg.ClientOptions(ctx)...)
	if err != nil {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("new_activation_client: %w", err)
	}

	// request activation of the OPC Simulator class for the IOPCServer interface.
	act, err := iact.RemoteActivation(ctx, &iactivation.RemoteActivationRequest{
		ORPCThis: &dcom.ORPCThis{Version: srv.COMVersion},
		ClassID:  dtyp.GUIDFromUUID(classID),
		IIDs:     []*dcom.IID{iopcserver.ServerIID},
		// for TCP/IP it must be []uint16{7} / for named pipes: []uint16{15}.
		RequestedProtocolSequences: []uint16{7, 15},
	})
	if err != nil {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("remote_activation: %w", err)
	}

	if act.HResult != 0 {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("remote_activation: %s", hresult.FromCode(uint32(act.HResult)))
	}

	// dial the activated object using the OXID bindings returned by the SCM
	// (use ncacn_ip_tcp).
	conn, err := dcerpc.Dial(ctx, cfg.ServerAddr(), append(cfg.DialOptions(ctx), act.OXIDBindings.EndpointsByProtocol("ncacn_ip_tcp")...)...)
	if err != nil {
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("dial_oxid_endpoint: %w", err)
	}

	// establish a fresh security context for the object connection.
	ctx = gssapi.NewSecurityContext(ctx)

	opc, err := opcda_client.NewClient(ctx, conn, cfg.ClientOptions(ctx)...)
	if err != nil {
		conn.Close(ctx)
		cc.Close(ctx)
		return nil, nil, nil, fmt.Errorf("new_opcda_client: %w", err)
	}

	// bind all opcda interfaces to the activated object instance.
	opc = opc.IPID(ctx, act.InterfaceData[0].IPID())

	// the initial well-known-endpoint connection is no longer needed.
	cc.Close(ctx)

	return conn, opc, &dcom.ORPCThis{Version: srv.COMVersion}, nil
}
