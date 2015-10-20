// Code generated by protoc-gen-go.
// source: traceservice.proto
// DO NOT EDIT!

/*
Package traceservice is a generated protocol buffer package.

It is generated from these files:
	traceservice.proto

It has these top-level messages:
	Empty
	CommitID
	Params
	MissingParamsRequest
	MissingParamsResponse
	AddParamsRequest
	StoredEntry
	AddRequest
	RemoveRequest
	ListRequest
	ListResponse
	GetValuesRequest
	GetValuesResponse
	GetParamsRequest
	GetParamsResponse
*/
package traceservice

import proto "github.com/golang/protobuf/proto"
import fmt "fmt"
import math "math"

import (
	context "golang.org/x/net/context"
	grpc "google.golang.org/grpc"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

type Empty struct {
}

func (m *Empty) Reset()         { *m = Empty{} }
func (m *Empty) String() string { return proto.CompactTextString(m) }
func (*Empty) ProtoMessage()    {}

// CommitID identifies one commit, or trybot try.
type CommitID struct {
	// The id of a commit, either a git hash, or a Reitveld patch id.
	Id string `protobuf:"bytes,1,opt,name=id" json:"id,omitempty"`
	// The source of the commit, either a git branch name, or a Reitveld issue id.
	Source string `protobuf:"bytes,2,opt,name=source" json:"source,omitempty"`
	// The timestamp of the commit or trybot patch.
	Timestamp int64 `protobuf:"varint,3,opt,name=timestamp" json:"timestamp,omitempty"`
}

func (m *CommitID) Reset()         { *m = CommitID{} }
func (m *CommitID) String() string { return proto.CompactTextString(m) }
func (*CommitID) ProtoMessage()    {}

// Params are the key-value pairs for a single trace.
//
// All of the key-value parameters should be present, the ones used to
// construct the traceid, along with optional parameters.
type Params struct {
	Params map[string]string `protobuf:"bytes,1,rep,name=params" json:"params,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
}

func (m *Params) Reset()         { *m = Params{} }
func (m *Params) String() string { return proto.CompactTextString(m) }
func (*Params) ProtoMessage()    {}

func (m *Params) GetParams() map[string]string {
	if m != nil {
		return m.Params
	}
	return nil
}

type MissingParamsRequest struct {
	Traceids []string `protobuf:"bytes,1,rep,name=traceids" json:"traceids,omitempty"`
}

func (m *MissingParamsRequest) Reset()         { *m = MissingParamsRequest{} }
func (m *MissingParamsRequest) String() string { return proto.CompactTextString(m) }
func (*MissingParamsRequest) ProtoMessage()    {}

type MissingParamsResponse struct {
	Traceids []string `protobuf:"bytes,1,rep,name=traceids" json:"traceids,omitempty"`
}

func (m *MissingParamsResponse) Reset()         { *m = MissingParamsResponse{} }
func (m *MissingParamsResponse) String() string { return proto.CompactTextString(m) }
func (*MissingParamsResponse) ProtoMessage()    {}

type AddParamsRequest struct {
	// maps traceid to the Params for that trace.
	Params map[string]*Params `protobuf:"bytes,1,rep,name=params" json:"params,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
}

func (m *AddParamsRequest) Reset()         { *m = AddParamsRequest{} }
func (m *AddParamsRequest) String() string { return proto.CompactTextString(m) }
func (*AddParamsRequest) ProtoMessage()    {}

func (m *AddParamsRequest) GetParams() map[string]*Params {
	if m != nil {
		return m.Params
	}
	return nil
}

// StoredEntry is used to serialize the Params to be stored in the BoltBD.
type StoredEntry struct {
	// The parameters for the trace.
	Params *Params `protobuf:"bytes,2,opt,name=params" json:"params,omitempty"`
}

func (m *StoredEntry) Reset()         { *m = StoredEntry{} }
func (m *StoredEntry) String() string { return proto.CompactTextString(m) }
func (*StoredEntry) ProtoMessage()    {}

func (m *StoredEntry) GetParams() *Params {
	if m != nil {
		return m.Params
	}
	return nil
}

type AddRequest struct {
	// The id of the commit/trybot we are adding data about.
	Commitid *CommitID `protobuf:"bytes,1,opt,name=commitid" json:"commitid,omitempty"`
	// A map of traceid to Entry.
	Entries map[string][]byte `protobuf:"bytes,2,rep,name=entries" json:"entries,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

func (m *AddRequest) Reset()         { *m = AddRequest{} }
func (m *AddRequest) String() string { return proto.CompactTextString(m) }
func (*AddRequest) ProtoMessage()    {}

func (m *AddRequest) GetCommitid() *CommitID {
	if m != nil {
		return m.Commitid
	}
	return nil
}

func (m *AddRequest) GetEntries() map[string][]byte {
	if m != nil {
		return m.Entries
	}
	return nil
}

type RemoveRequest struct {
	// The id of the commit/trybot we are removing.
	Commitid *CommitID `protobuf:"bytes,1,opt,name=commitid" json:"commitid,omitempty"`
}

func (m *RemoveRequest) Reset()         { *m = RemoveRequest{} }
func (m *RemoveRequest) String() string { return proto.CompactTextString(m) }
func (*RemoveRequest) ProtoMessage()    {}

func (m *RemoveRequest) GetCommitid() *CommitID {
	if m != nil {
		return m.Commitid
	}
	return nil
}

type ListRequest struct {
	// begin is the unix timestamp to start searching from.
	Begin int64 `protobuf:"varint,1,opt,name=begin" json:"begin,omitempty"`
	// end is the unix timestamp to search to (inclusive).
	End int64 `protobuf:"varint,2,opt,name=end" json:"end,omitempty"`
}

func (m *ListRequest) Reset()         { *m = ListRequest{} }
func (m *ListRequest) String() string { return proto.CompactTextString(m) }
func (*ListRequest) ProtoMessage()    {}

type ListResponse struct {
	// A list of CommitIDs that fall between the given timestamps in
	// ListRequest.
	Commitids []*CommitID `protobuf:"bytes,3,rep,name=commitids" json:"commitids,omitempty"`
}

func (m *ListResponse) Reset()         { *m = ListResponse{} }
func (m *ListResponse) String() string { return proto.CompactTextString(m) }
func (*ListResponse) ProtoMessage()    {}

func (m *ListResponse) GetCommitids() []*CommitID {
	if m != nil {
		return m.Commitids
	}
	return nil
}

type GetValuesRequest struct {
	Commitid *CommitID `protobuf:"bytes,1,opt,name=commitid" json:"commitid,omitempty"`
}

func (m *GetValuesRequest) Reset()         { *m = GetValuesRequest{} }
func (m *GetValuesRequest) String() string { return proto.CompactTextString(m) }
func (*GetValuesRequest) ProtoMessage()    {}

func (m *GetValuesRequest) GetCommitid() *CommitID {
	if m != nil {
		return m.Commitid
	}
	return nil
}

type GetValuesResponse struct {
	// Maps traceid's to their []byte serialized values.
	Values map[string][]byte `protobuf:"bytes,3,rep,name=values" json:"values,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

func (m *GetValuesResponse) Reset()         { *m = GetValuesResponse{} }
func (m *GetValuesResponse) String() string { return proto.CompactTextString(m) }
func (*GetValuesResponse) ProtoMessage()    {}

func (m *GetValuesResponse) GetValues() map[string][]byte {
	if m != nil {
		return m.Values
	}
	return nil
}

type GetParamsRequest struct {
	// A list of traceids.
	Traceids []string `protobuf:"bytes,1,rep,name=traceids" json:"traceids,omitempty"`
}

func (m *GetParamsRequest) Reset()         { *m = GetParamsRequest{} }
func (m *GetParamsRequest) String() string { return proto.CompactTextString(m) }
func (*GetParamsRequest) ProtoMessage()    {}

type GetParamsResponse struct {
	// Maps traceids to their Params.
	Params map[string]*Params `protobuf:"bytes,3,rep,name=params" json:"params,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
}

func (m *GetParamsResponse) Reset()         { *m = GetParamsResponse{} }
func (m *GetParamsResponse) String() string { return proto.CompactTextString(m) }
func (*GetParamsResponse) ProtoMessage()    {}

func (m *GetParamsResponse) GetParams() map[string]*Params {
	if m != nil {
		return m.Params
	}
	return nil
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// Client API for TraceService service

type TraceServiceClient interface {
	// Returns a list of traceids that don't have Params stored in the datastore.
	MissingParams(ctx context.Context, in *MissingParamsRequest, opts ...grpc.CallOption) (*MissingParamsResponse, error)
	// Adds Params for a set of traceids.
	AddParams(ctx context.Context, in *AddParamsRequest, opts ...grpc.CallOption) (*Empty, error)
	// Adds data for a set of traces for a particular commitid.
	Add(ctx context.Context, in *AddRequest, opts ...grpc.CallOption) (*Empty, error)
	// Removes data for a particular commitid.
	Remove(ctx context.Context, in *RemoveRequest, opts ...grpc.CallOption) (*Empty, error)
	// List returns all the CommitIDs that exist in the given time range.
	List(ctx context.Context, in *ListRequest, opts ...grpc.CallOption) (*ListResponse, error)
	// GetValues returns all the trace values stored for the given CommitID.
	GetValues(ctx context.Context, in *GetValuesRequest, opts ...grpc.CallOption) (*GetValuesResponse, error)
	// GetParams returns the Params for all of the given traces.
	GetParams(ctx context.Context, in *GetParamsRequest, opts ...grpc.CallOption) (*GetParamsResponse, error)
	// Ping returns the Params for all of the given traces.
	Ping(ctx context.Context, in *GetParamsRequest, opts ...grpc.CallOption) (*Empty, error)
}

type traceServiceClient struct {
	cc *grpc.ClientConn
}

func NewTraceServiceClient(cc *grpc.ClientConn) TraceServiceClient {
	return &traceServiceClient{cc}
}

func (c *traceServiceClient) MissingParams(ctx context.Context, in *MissingParamsRequest, opts ...grpc.CallOption) (*MissingParamsResponse, error) {
	out := new(MissingParamsResponse)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/MissingParams", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) AddParams(ctx context.Context, in *AddParamsRequest, opts ...grpc.CallOption) (*Empty, error) {
	out := new(Empty)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/AddParams", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) Add(ctx context.Context, in *AddRequest, opts ...grpc.CallOption) (*Empty, error) {
	out := new(Empty)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/Add", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) Remove(ctx context.Context, in *RemoveRequest, opts ...grpc.CallOption) (*Empty, error) {
	out := new(Empty)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/Remove", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) List(ctx context.Context, in *ListRequest, opts ...grpc.CallOption) (*ListResponse, error) {
	out := new(ListResponse)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/List", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) GetValues(ctx context.Context, in *GetValuesRequest, opts ...grpc.CallOption) (*GetValuesResponse, error) {
	out := new(GetValuesResponse)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/GetValues", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) GetParams(ctx context.Context, in *GetParamsRequest, opts ...grpc.CallOption) (*GetParamsResponse, error) {
	out := new(GetParamsResponse)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/GetParams", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *traceServiceClient) Ping(ctx context.Context, in *GetParamsRequest, opts ...grpc.CallOption) (*Empty, error) {
	out := new(Empty)
	err := grpc.Invoke(ctx, "/traceservice.TraceService/Ping", in, out, c.cc, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Server API for TraceService service

type TraceServiceServer interface {
	// Returns a list of traceids that don't have Params stored in the datastore.
	MissingParams(context.Context, *MissingParamsRequest) (*MissingParamsResponse, error)
	// Adds Params for a set of traceids.
	AddParams(context.Context, *AddParamsRequest) (*Empty, error)
	// Adds data for a set of traces for a particular commitid.
	Add(context.Context, *AddRequest) (*Empty, error)
	// Removes data for a particular commitid.
	Remove(context.Context, *RemoveRequest) (*Empty, error)
	// List returns all the CommitIDs that exist in the given time range.
	List(context.Context, *ListRequest) (*ListResponse, error)
	// GetValues returns all the trace values stored for the given CommitID.
	GetValues(context.Context, *GetValuesRequest) (*GetValuesResponse, error)
	// GetParams returns the Params for all of the given traces.
	GetParams(context.Context, *GetParamsRequest) (*GetParamsResponse, error)
	// Ping returns the Params for all of the given traces.
	Ping(context.Context, *GetParamsRequest) (*Empty, error)
}

func RegisterTraceServiceServer(s *grpc.Server, srv TraceServiceServer) {
	s.RegisterService(&_TraceService_serviceDesc, srv)
}

func _TraceService_MissingParams_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(MissingParamsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).MissingParams(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_AddParams_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(AddParamsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).AddParams(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_Add_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(AddRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).Add(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_Remove_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(RemoveRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).Remove(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_List_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(ListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).List(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_GetValues_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(GetValuesRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).GetValues(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_GetParams_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(GetParamsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).GetParams(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func _TraceService_Ping_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error) (interface{}, error) {
	in := new(GetParamsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	out, err := srv.(TraceServiceServer).Ping(ctx, in)
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _TraceService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "traceservice.TraceService",
	HandlerType: (*TraceServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "MissingParams",
			Handler:    _TraceService_MissingParams_Handler,
		},
		{
			MethodName: "AddParams",
			Handler:    _TraceService_AddParams_Handler,
		},
		{
			MethodName: "Add",
			Handler:    _TraceService_Add_Handler,
		},
		{
			MethodName: "Remove",
			Handler:    _TraceService_Remove_Handler,
		},
		{
			MethodName: "List",
			Handler:    _TraceService_List_Handler,
		},
		{
			MethodName: "GetValues",
			Handler:    _TraceService_GetValues_Handler,
		},
		{
			MethodName: "GetParams",
			Handler:    _TraceService_GetParams_Handler,
		},
		{
			MethodName: "Ping",
			Handler:    _TraceService_Ping_Handler,
		},
	},
	Streams: []grpc.StreamDesc{},
}