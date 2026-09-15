package iopcsyncio

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/oiweiwei/go-msrpc/dcerpc"
	"github.com/oiweiwei/go-msrpc/msrpc/dcom"
	"github.com/oiweiwei/go-msrpc/ndr"
	"github.com/oiweiwei/go-opcda/opc/opcda"
)

// responseConn replaces only the transport: the generated operation still
// decodes a real NDR response, including input-dependent array counts.
type responseConn struct {
	dcerpc.Conn
	response []byte
	err      error
}

func (c *responseConn) Invoke(ctx context.Context, op dcerpc.Operation, _ ...dcerpc.CallOption) error {
	if c.err != nil {
		return c.err
	}
	return op.UnmarshalNDRResponse(ctx, ndr.NDR20(c.response))
}

func (c *responseConn) Error(_ context.Context, value any) error {
	return fmt.Errorf("HRESULT %#x", uint32(value.(int32)))
}

func TestSyncIOHRESULT(t *testing.T) {
	transportErr := errors.New("transport failed")
	for _, tc := range []struct {
		name      string
		status    int32
		transport error
		wantErr   bool
	}{
		{"S_OK", 0, nil, false},
		{"S_FALSE", 1, nil, false},
		{"E_FAIL", -2147467259, nil, true},
		{"transport", 0, transportErr, true},
	} {
		for _, method := range []string{"Read", "Write"} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				itemErrors := []int32{0, -1073479673} // OPC_E_UNKNOWNITEMID
				var wire []byte
				var err error
				if method == "Read" {
					wire, err = ndr.Marshal(&ReadResponse{Count: 2, Return: tc.status, Errors: itemErrors,
						ItemValues: []*opcda.ItemState{{Client: 42, Quality: 192}, {Client: 43}}})
				} else {
					wire, err = ndr.Marshal(&WriteResponse{Count: 2, Return: tc.status, Errors: itemErrors})
				}
				if err != nil {
					t.Fatal(err)
				}
				client := &xxx_DefaultSyncIOClient{cc: &responseConn{response: wire, err: tc.transport}, ipid: &dcom.IPID{}}
				var status int32
				var gotErrors []int32
				var nilResponse bool
				if method == "Read" {
					out, callErr := client.Read(ctx, &ReadRequest{Count: 2, Server: []uint32{1, 2}})
					err, nilResponse = callErr, out == nil
					if out != nil {
						status, gotErrors = out.Return, out.Errors
						if len(out.ItemValues) != 2 || out.ItemValues[0].Client != 42 || out.ItemValues[0].Quality != 192 || out.ItemValues[1].Client != 43 {
							t.Fatalf("Read lost item values: %+v", out.ItemValues)
						}
					}
				} else {
					out, callErr := client.Write(ctx, &WriteRequest{Count: 2, Server: []uint32{1, 2}})
					err, nilResponse = callErr, out == nil
					if out != nil {
						status, gotErrors = out.Return, out.Errors
					}
				}
				if (err != nil) != tc.wantErr {
					t.Errorf("error = %v, wantErr %v", err, tc.wantErr)
				}
				if tc.transport != nil {
					if !errors.Is(err, transportErr) || !nilResponse {
						t.Fatalf("transport failure = (%v, %v)", nilResponse, err)
					}
					return
				}
				if nilResponse || status != tc.status || len(gotErrors) != 2 || gotErrors[0] != 0 || gotErrors[1] != -1073479673 {
					t.Fatalf("response lost status or per-item results: nil=%v status=%d errors=%v", nilResponse, status, gotErrors)
				}
			})
		}
	}
}
