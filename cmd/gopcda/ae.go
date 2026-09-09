// A&E (OPC Alarms & Events) support for gopcda.
//
// OPC A&E event delivery is push-based: a real subscription requires the server
// to call back into a client-hosted IOPCEventSink, which this cross-platform
// (non-COM) client does not host. Everything reachable without that callback is
// a pull, and that is what these subcommands do:
//
//	ae enum       - discover which A&E servers are registered on the host
//	ae status     - IOPCEventServer::GetStatus
//	ae categories - IOPCEventServer::QueryEventCategories
//	ae browse     - walk the area/source space (IOPCEventAreaBrowser)
//	ae conditions - pull current condition/alarm state (QuerySourceConditions +
//	                GetConditionState) for the given (or all discovered) sources
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/oiweiwei/go-msrpc/midl/uuid"

	"github.com/oiweiwei/go-msrpc/msrpc/dcom"
	"github.com/oiweiwei/go-msrpc/msrpc/dtyp"
	"github.com/oiweiwei/go-msrpc/msrpc/erref/hresult"

	"github.com/oiweiwei/go-opcda/opc/opcae"
	opcae_client "github.com/oiweiwei/go-opcda/opc/opcae/client"
	iopceventareabrowser "github.com/oiweiwei/go-opcda/opc/opcae/iopceventareabrowser/v0"
	iopceventserver "github.com/oiweiwei/go-opcda/opc/opcae/iopceventserver/v0"

	opccomn_client "github.com/oiweiwei/go-opcda/opc/opccomn/client"
	iopcserverlist "github.com/oiweiwei/go-opcda/opc/opccomn/iopcserverlist/v0"

	"github.com/oiweiwei/go-opcda/opc/sdk/comcat"
	ienumguid "github.com/oiweiwei/go-opcda/opc/sdk/comcat/ienumguid/v0"
)

const (
	// OpcEnum service CLSID: the local server that enumerates registered OPC
	// servers by component category (IOPCServerList / IOPCServerList2).
	opcEnumCLSID = "13486D51-4821-11D2-A494-3CB306C10000"
	// CATID_OPCAEServer10: the component category all OPC A&E 1.0 servers
	// register under.
	catidOPCAEServer10 = "58E13251-AC87-11D1-84D5-00608CB8A7E9"

	// OPC event type bit flags (OPC_SIMPLE_EVENT | OPC_TRACKING_EVENT |
	// OPC_CONDITION_EVENT); passed to QueryEventCategories to request all.
	opcAllEvents = 0x7

	// OPC condition state bit flags (wState in OPCCONDITIONSTATE).
	condEnabled = 0x0001
	condActive  = 0x0002
	condAcked   = 0x0004
)

// aeClassID is the A&E server CLSID to activate; empty means auto-discover via
// OpcEnum and use the first A&E server found.
var aeClassID string

func aeCmd() *cobra.Command {
	ae := &cobra.Command{
		Use:   "ae",
		Short: "OPC Alarms & Events (A&E) client over DCOM (pull operations)",
	}
	ae.PersistentFlags().StringVar(&aeClassID, "ae-class-id", "",
		"CLSID of the OPC A&E server to activate (default: auto-discover via OpcEnum)")
	ae.AddCommand(aeEnumCmd(), aeStatusCmd(), aeCategoriesCmd(), aeBrowseCmd(), aeConditionsCmd())
	return ae
}

// ---------------------------------------------------------------------------
// ae session: an activated OPC A&E server (IOPCEventServer)
// ---------------------------------------------------------------------------

type aeSession struct {
	*activation
	ae opcae_client.Client
}

// aeConnect activates the A&E server for its IOPCEventServer interface. The
// CLSID is taken from --ae-class-id, or discovered via OpcEnum when that flag
// is empty.
func aeConnect(ctx context.Context, targetSpec string) (*aeSession, error) {
	var clsID *uuid.UUID
	if aeClassID != "" {
		c, err := uuid.Parse(aeClassID)
		if err != nil {
			return nil, fmt.Errorf("parse ae-class-id: %w", err)
		}
		clsID = c
	} else {
		servers, err := discoverAEServers(ctx, targetSpec)
		if err != nil {
			return nil, fmt.Errorf("discover A&E server: %w", err)
		}
		if len(servers) == 0 {
			return nil, fmt.Errorf("no OPC A&E servers registered on host; pass --ae-class-id")
		}
		c, err := uuid.Parse(servers[0].CLSID)
		if err != nil {
			return nil, fmt.Errorf("parse discovered CLSID %q: %w", servers[0].CLSID, err)
		}
		clsID = c
		fmt.Fprintf(os.Stderr, "using A&E server %q (%s)\n", servers[0].ProgID, servers[0].CLSID)
	}

	a, err := activate(ctx, targetSpec, clsID, iopceventserver.EventServerIID)
	if err != nil {
		return nil, err
	}

	c, err := opcae_client.NewClient(ctx, a.conn, a.authOpt)
	if err != nil {
		a.Close(ctx)
		return nil, fmt.Errorf("new_opcae_client: %w", err)
	}
	a.ru = c.RemoteUnknown2()

	return &aeSession{activation: a, ae: c}, nil
}

// server returns the IOPCEventServer client bound to the activated object.
func (s *aeSession) server(ctx context.Context) iopceventserver.EventServerClient {
	return s.ae.IPID(ctx, s.serverIPID).EventServer()
}

// areaBrowser creates an IOPCEventAreaBrowser on the server and returns a client
// bound to it. The pointer returned by CreateAreaBrowser is already the area
// browser interface (riid), so no further QueryInterface is needed.
func (s *aeSession) areaBrowser(ctx context.Context) (iopceventareabrowser.EventAreaBrowserClient, error) {
	res, err := s.server(ctx).CreateAreaBrowser(ctx, &iopceventserver.CreateAreaBrowserRequest{
		This: s.this,
		RIID: iopceventareabrowser.EventAreaBrowserIID,
	})
	if err != nil {
		return nil, fmt.Errorf("create_area_browser: %w", err)
	}
	if res.Return != 0 {
		return nil, fmt.Errorf("create_area_browser: %s", hresult.FromCode(uint32(res.Return)))
	}
	if res.Unknown == nil {
		return nil, fmt.Errorf("create_area_browser: server returned no interface pointer")
	}
	ipid := res.Unknown.InterfacePointer().IPID()
	return s.ae.IPID(ctx, ipid).EventAreaBrowser(), nil
}

// ---------------------------------------------------------------------------
// ae enum: discover A&E servers via OpcEnum
// ---------------------------------------------------------------------------

type aeServer struct {
	CLSID  string `json:"clsid"`
	ProgID string `json:"prog_id,omitempty"`
}

// discoverAEServers activates the OpcEnum service and lists every CLSID
// registered under the OPC A&E 1.0 component category, resolving each to its
// ProgID.
func discoverAEServers(ctx context.Context, targetSpec string) ([]aeServer, error) {
	clsID, err := uuid.Parse(opcEnumCLSID)
	if err != nil {
		return nil, err
	}

	a, err := activate(ctx, targetSpec, clsID, iopcserverlist.ServerListIID)
	if err != nil {
		return nil, err
	}
	defer a.Close(ctx)

	common, err := opccomn_client.NewClient(ctx, a.conn, a.authOpt)
	if err != nil {
		return nil, fmt.Errorf("new_opccomn_client: %w", err)
	}
	a.ru = common.RemoteUnknown2()
	list := common.ServerList().IPID(ctx, a.serverIPID)

	catUUID, err := uuid.Parse(catidOPCAEServer10)
	if err != nil {
		return nil, err
	}
	cat := (*comcat.CategoryID)(dtyp.GUIDFromUUID(catUUID))

	res, err := list.EnumClassesOfCategories(ctx, &iopcserverlist.EnumClassesOfCategoriesRequest{
		This:                   a.this,
		ImplementedCount:       1,
		CategoryIDsImplemented: []*comcat.CategoryID{cat},
	})
	if err != nil {
		return nil, fmt.Errorf("enum_classes_of_categories: %w", err)
	}
	if res.Return != 0 {
		return nil, fmt.Errorf("enum_classes_of_categories: %s", hresult.FromCode(uint32(res.Return)))
	}
	if res.ClassID == nil {
		return nil, nil
	}

	guids, err := a.enumGUIDs(ctx, res.ClassID.InterfacePointer().IPID())
	if err != nil {
		return nil, err
	}

	servers := make([]aeServer, 0, len(guids))
	for _, g := range guids {
		if g == nil {
			continue
		}
		s := aeServer{CLSID: g.UUID().String()}
		det, err := list.GetClassDetails(ctx, &iopcserverlist.GetClassDetailsRequest{
			This:    a.this,
			ClassID: (*dcom.ClassID)(g),
		})
		if err == nil && det.Return == 0 {
			s.ProgID = det.ProgrammaticID
		}
		servers = append(servers, s)
	}
	return servers, nil
}

// enumGUIDs drains an IEnumGUID enumerator (returned by
// EnumClassesOfCategories).
func (a *activation) enumGUIDs(ctx context.Context, ipid *dcom.IPID) ([]*dtyp.GUID, error) {
	eg, err := ienumguid.NewIEnumGUIDClient(ctx, a.conn, a.authOpt)
	if err != nil {
		return nil, fmt.Errorf("new_enum_guid: %w", err)
	}
	eg = eg.IPID(ctx, ipid)

	const batch = 64
	var out []*dtyp.GUID
	for {
		next, err := eg.Next(ctx, &ienumguid.NextRequest{This: a.this, Count: batch})
		// S_FALSE (mapped to ErrorArithmeticOverflow) means the enumerator
		// returned fewer than requested - the last, partial batch.
		if err != nil && !errors.Is(err, hresult.ErrorArithmeticOverflow) {
			return nil, fmt.Errorf("enum_guid_next: %w", err)
		}
		if next == nil {
			break
		}
		out = append(out, next.Elements...)
		if next.Fetched < batch {
			break
		}
	}
	return out, nil
}

func aeEnumCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enum <target>",
		Short: "Discover OPC A&E servers registered on the host (via OpcEnum)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			servers, err := discoverAEServers(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return encode(servers)
		},
	}
}

// ---------------------------------------------------------------------------
// ae status
// ---------------------------------------------------------------------------

func aeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <target>",
		Short: "Activate the A&E server and print IOPCEventServer::GetStatus",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := aeConnect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			st, err := s.server(ctx).GetStatus(ctx, &iopceventserver.GetStatusRequest{This: s.this})
			if err != nil {
				return fmt.Errorf("get_status: %w", err)
			}
			return encode(st.EventServerStatus)
		},
	}
}

// ---------------------------------------------------------------------------
// ae categories
// ---------------------------------------------------------------------------

func aeCategoriesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "categories <target>",
		Short: "List event categories with their conditions and attributes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := aeConnect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			srv := s.server(ctx)
			res, err := srv.QueryEventCategories(ctx, &iopceventserver.QueryEventCategoriesRequest{
				This:      s.this,
				EventType: opcAllEvents,
			})
			if err != nil {
				return fmt.Errorf("query_event_categories: %w", err)
			}

			out := make([]map[string]any, 0, len(res.EventCategories))
			for i, id := range res.EventCategories {
				row := map[string]any{"id": id}
				if i < len(res.EventCategoryDescriptions) {
					row["description"] = res.EventCategoryDescriptions[i]
				}

				// enrich with per-category conditions and attributes; both are
				// optional (condition-only servers implement one, not the
				// other), so E_NOTIMPL and friends are non-fatal.
				if cn, err := srv.QueryConditionNames(ctx, &iopceventserver.QueryConditionNamesRequest{
					This:          s.this,
					EventCategory: id,
				}); err == nil && len(cn.ConditionNames) > 0 {
					row["conditions"] = cn.ConditionNames
				}

				if at, err := srv.QueryEventAttributes(ctx, &iopceventserver.QueryEventAttributesRequest{
					This:          s.this,
					EventCategory: id,
				}); err == nil && len(at.AttributeIDs) > 0 {
					attrs := make([]map[string]any, 0, len(at.AttributeIDs))
					for j, aid := range at.AttributeIDs {
						a := map[string]any{"id": aid}
						if j < len(at.AttributeDescriptions) {
							a["description"] = at.AttributeDescriptions[j]
						}
						if j < len(at.AttributeTypes) {
							a["type"] = at.AttributeTypes[j]
						}
						attrs = append(attrs, a)
					}
					row["attributes"] = attrs
				}

				out = append(out, row)
			}
			return encode(out)
		},
	}
}

// ---------------------------------------------------------------------------
// ae browse
// ---------------------------------------------------------------------------

func aeBrowseCmd() *cobra.Command {
	var pos string
	cmd := &cobra.Command{
		Use:   "browse <target>",
		Short: "Browse the A&E area/source space at the given position",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := aeConnect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			browser, err := s.areaBrowser(ctx)
			if err != nil {
				return err
			}

			if pos != "" {
				if _, err := browser.ChangeBrowsePosition(ctx, &iopceventareabrowser.ChangeBrowsePositionRequest{
					This:            s.this,
					BrowseDirection: opcae.BrowseDirectionTo,
					String:          pos,
				}); err != nil {
					return fmt.Errorf("change_browse_position: %w", err)
				}
			}

			areas, err := s.browseNames(ctx, browser, opcae.BrowseTypeArea)
			if err != nil {
				return err
			}
			sources, err := s.browseNames(ctx, browser, opcae.BrowseTypeSource)
			if err != nil {
				return err
			}

			// resolve sources to fully-qualified names usable by `ae conditions`.
			qualified := make([]string, 0, len(sources))
			for _, src := range sources {
				q, err := browser.GetQualifiedSourceName(ctx, &iopceventareabrowser.GetQualifiedSourceNameRequest{
					This:       s.this,
					SourceName: src,
				})
				if err == nil && q.QualifiedSourceName != "" {
					qualified = append(qualified, q.QualifiedSourceName)
				} else {
					qualified = append(qualified, src)
				}
			}

			return encode(map[string]any{"areas": areas, "sources": qualified})
		},
	}
	cmd.Flags().StringVar(&pos, "position", "", "move to this area before browsing")
	return cmd
}

// browseNames browses area or source names at the browser's current position.
func (s *aeSession) browseNames(ctx context.Context, browser iopceventareabrowser.EventAreaBrowserClient, typ opcae.BrowseType) ([]string, error) {
	res, err := browser.BrowseOPCAreas(ctx, &iopceventareabrowser.BrowseOPCAreasRequest{
		This:             s.this,
		BrowseFilterType: typ,
	})
	if err != nil {
		return nil, fmt.Errorf("browse_opc_areas: %w", err)
	}
	if res.IEnumString == nil {
		return nil, nil
	}
	return s.enumStrings(ctx, res.IEnumString.InterfacePointer().IPID())
}

// ---------------------------------------------------------------------------
// ae conditions
// ---------------------------------------------------------------------------

func aeConditionsCmd() *cobra.Command {
	var (
		all        bool
		activeOnly bool
	)
	cmd := &cobra.Command{
		Use:   "conditions <target> [SOURCE...]",
		Short: "Pull current condition/alarm state for sources (or all with --all)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sources := args[1:]
			if len(sources) == 0 && !all {
				return fmt.Errorf("provide one or more SOURCE names, or --all to discover them")
			}

			s, err := aeConnect(ctx, args[0])
			if err != nil {
				return err
			}
			defer s.Close(ctx)

			if all {
				browser, err := s.areaBrowser(ctx)
				if err != nil {
					return err
				}
				discovered, err := s.walkSources(ctx, browser, 0)
				if err != nil {
					return fmt.Errorf("discover sources: %w", err)
				}
				sources = append(sources, discovered...)
			}

			srv := s.server(ctx)
			out := make([]map[string]any, 0)
			for _, src := range sources {
				qc, err := srv.QuerySourceConditions(ctx, &iopceventserver.QuerySourceConditionsRequest{
					This:   s.this,
					Source: src,
				})
				if err != nil {
					out = append(out, map[string]any{"source": src, "error": err.Error()})
					continue
				}

				for _, name := range qc.ConditionNames {
					cs, err := srv.GetConditionState(ctx, &iopceventserver.GetConditionStateRequest{
						This:          s.this,
						Source:        src,
						ConditionName: name,
					})
					if err != nil {
						out = append(out, map[string]any{"source": src, "condition": name, "error": err.Error()})
						continue
					}
					row := conditionRow(src, name, cs.ConditionState)
					if activeOnly && row["active"] != true {
						continue
					}
					out = append(out, row)
				}
			}
			return encode(out)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "auto-discover all sources by walking the area tree")
	cmd.Flags().BoolVar(&activeOnly, "active", false, "only report conditions currently in the active state")
	return cmd
}

// conditionRow flattens an OPCCONDITIONSTATE into a JSON-friendly row.
func conditionRow(source, name string, cs *opcae.ConditionState) map[string]any {
	row := map[string]any{
		"source":    source,
		"condition": name,
	}
	if cs == nil {
		return row
	}
	row["state"] = cs.State
	row["enabled"] = cs.State&condEnabled != 0
	row["active"] = cs.State&condActive != 0
	row["acked"] = cs.State&condAcked != 0
	if cs.ActiveSubcondition != "" {
		row["active_subcondition"] = cs.ActiveSubcondition
	}
	if cs.ActiveSubconditionSeverity != 0 {
		row["severity"] = cs.ActiveSubconditionSeverity
	}
	if cs.ActiveSubconditionDescription != "" {
		row["message"] = cs.ActiveSubconditionDescription
	}
	row["quality"] = cs.Quality
	if t := ftime(cs.ConditionLastActive); t != nil {
		row["last_active"] = t
	}
	if t := ftime(cs.ConditionLastInactive); t != nil {
		row["last_inactive"] = t
	}
	if t := ftime(cs.LastAckTime); t != nil {
		row["last_ack"] = t
	}
	if cs.AcknowledgerID != "" {
		row["acknowledger"] = cs.AcknowledgerID
	}
	if cs.Comment != "" {
		row["comment"] = cs.Comment
	}
	return row
}

// walkSources recursively browses the area tree from the browser's current
// position, collecting the fully-qualified name of every source it finds.
func (s *aeSession) walkSources(ctx context.Context, browser iopceventareabrowser.EventAreaBrowserClient, depth int) ([]string, error) {
	const maxDepth = 32
	if depth > maxDepth {
		return nil, nil
	}

	sources, err := s.browseNames(ctx, browser, opcae.BrowseTypeSource)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, src := range sources {
		q, err := browser.GetQualifiedSourceName(ctx, &iopceventareabrowser.GetQualifiedSourceNameRequest{
			This:       s.this,
			SourceName: src,
		})
		if err == nil && q.QualifiedSourceName != "" {
			out = append(out, q.QualifiedSourceName)
		} else {
			out = append(out, src)
		}
	}

	// collect child areas first, then descend into each (the enumerator is
	// invalidated once we move the browse position).
	areas, err := s.browseNames(ctx, browser, opcae.BrowseTypeArea)
	if err != nil {
		return nil, err
	}
	for _, area := range areas {
		if _, err := browser.ChangeBrowsePosition(ctx, &iopceventareabrowser.ChangeBrowsePositionRequest{
			This:            s.this,
			BrowseDirection: opcae.BrowseDirectionDown,
			String:          area,
		}); err != nil {
			return nil, fmt.Errorf("change_browse_position down %q: %w", area, err)
		}
		child, err := s.walkSources(ctx, browser, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, child...)
		if _, err := browser.ChangeBrowsePosition(ctx, &iopceventareabrowser.ChangeBrowsePositionRequest{
			This:            s.this,
			BrowseDirection: opcae.BrowseDirectionUp,
		}); err != nil {
			return nil, fmt.Errorf("change_browse_position up: %w", err)
		}
	}
	return out, nil
}

// ftime renders a FILETIME as a JSON-friendly value: nil when unset, "never"
// for the sentinel, otherwise a time.Time.
func ftime(ft *dtyp.Filetime) any {
	if ft == nil || ft.IsZero() {
		return nil
	}
	if ft.IsNever() {
		return "never"
	}
	return ft.AsTime()
}
