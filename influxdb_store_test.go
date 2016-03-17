package appdash

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	influxDBServer "github.com/influxdata/influxdb/cmd/influxd/run"
)

const (
	clientEventKey    string = schemaPrefix + clientEventSchema
	clientEventSchema string = "HTTPClient"
	serverEventKey    string = schemaPrefix + serverEventSchema
	serverEventSchema string = "HTTPServer"
	spanNameSchema    string = "name"
)

func TestMergeSchemasField(t *testing.T) {
	cases := []struct {
		NewField string
		OldField string
		Want     string
	}{
		{NewField: "", OldField: "", Want: ""},
		{NewField: "HTTPClient", OldField: "", Want: "HTTPClient"},
		{NewField: "", OldField: "name", Want: "name"},
		{NewField: "HTTPClient", OldField: "name", Want: "HTTPClient,name"},
		{NewField: "HTTPServer", OldField: "HTTPClient,name", Want: "HTTPServer,HTTPClient,name"},
	}
	for i, c := range cases {
		got, err := mergeSchemasField(c.NewField, c.OldField)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got = sortSchemas(got)
		want := sortSchemas(c.Want)
		if got != want {
			t.Fatalf("case #%d - got: %v, want: %v", i, got, c.Want)
		}
	}
}

func TestSchemasFromAnnotations(t *testing.T) {
	anns := []Annotation{
		Annotation{Key: schemaPrefix + "HTTPClient"},
		Annotation{Key: schemaPrefix + "HTTPServer"},
		Annotation{Key: schemaPrefix + "name"},
	}
	got := sortSchemas(schemasFromAnnotations(anns))
	want := sortSchemas("HTTPClient,HTTPServer,name")
	if got != want {
		t.Fatalf("got: %v, want: %v", got, want)
	}
}

func TestFindTraceParent(t *testing.T) {
	trace := Trace{
		Span: Span{
			ID: SpanID{Trace: 1, Span: 100, Parent: 0},
		},
		Sub: []*Trace{
			&Trace{
				Span: Span{
					ID: SpanID{Trace: 1, Span: 11, Parent: 100},
				},
				Sub: []*Trace{
					&Trace{
						Span: Span{
							ID: SpanID{Trace: 1, Span: 111, Parent: 11},
						},
						Sub: []*Trace{
							&Trace{
								Span: Span{
									ID: SpanID{Trace: 1, Span: 1111, Parent: 111},
								},
							},
						},
					},
					&Trace{
						Span: Span{
							ID: SpanID{Trace: 1, Span: 112, Parent: 11},
						},
						Sub: []*Trace{
							&Trace{
								Span: Span{
									ID: SpanID{Trace: 1, Span: 1112, Parent: 112},
								},
							},
						},
					},
				},
			},
		},
	}
	cases := []struct {
		Parent *Trace
		Child  *Trace
	}{
		{nil, &trace},
		{nil, &Trace{}},
		{&trace, trace.Sub[0]},
		{trace.Sub[0], trace.Sub[0].Sub[0]},
		{trace.Sub[0], trace.Sub[0].Sub[1]},
		{trace.Sub[0].Sub[0], trace.Sub[0].Sub[0].Sub[0]},
		{trace.Sub[0].Sub[1], trace.Sub[0].Sub[1].Sub[0]},
	}
	for i, c := range cases {
		got := findTraceParent(&trace, c.Child)
		if got != c.Parent {
			t.Fatalf("case: %d - got: %v, want: %v", i, got, c.Parent)
		}
	}
}

func TestInfluxDBStore(t *testing.T) {
	store := newStore(t)
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	traces := []*Trace{
		&Trace{
			Span: Span{
				ID: SpanID{1, 100, 0},
				Annotations: Annotations{
					Annotation{Key: "Name", Value: []byte("/")},
					Annotation{Key: "Server.Request.Method", Value: []byte("GET")},
					Annotation{Key: clientEventKey, Value: []byte("")},
					Annotation{Key: serverEventKey, Value: []byte("")},
				},
			},
			Sub: []*Trace{
				&Trace{
					Span: Span{
						ID: SpanID{Trace: 1, Span: 11, Parent: 100},
						Annotations: Annotations{
							Annotation{Key: "Name", Value: []byte("localhost:8699/endpoint")},
							Annotation{Key: "Server.Request.Method", Value: []byte("GET")},
							Annotation{Key: clientEventKey, Value: []byte("")},
							Annotation{Key: serverEventKey, Value: []byte("")},
						},
					},
					Sub: []*Trace{
						&Trace{
							Span: Span{
								ID: SpanID{Trace: 1, Span: 111, Parent: 11},
								Annotations: Annotations{
									Annotation{Key: "Name", Value: []byte("localhost:8699/sub1")},
									Annotation{Key: "Server.Request.Method", Value: []byte("GET")},
									Annotation{Key: clientEventKey, Value: []byte("")},
									Annotation{Key: serverEventKey, Value: []byte("")},
								},
							},
							Sub: []*Trace{
								&Trace{
									Span: Span{
										ID: SpanID{Trace: 1, Span: 1111, Parent: 111},
										Annotations: Annotations{
											Annotation{Key: "Name", Value: []byte("localhost:8699/sub2")},
											Annotation{Key: "Server.Request.Method", Value: []byte("GET")},
											Annotation{Key: clientEventKey, Value: []byte("")},
											Annotation{Key: serverEventKey, Value: []byte("")},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		&Trace{
			Span: Span{
				ID: SpanID{2, 200, 0},
				Annotations: Annotations{
					Annotation{Key: "Name", Value: []byte("/")},
					Annotation{Key: "Server.Request.Method", Value: []byte("GET")},
					Annotation{Key: clientEventKey, Value: []byte("")},
					Annotation{Key: serverEventKey, Value: []byte("")},
				},
			},
		},
	}

	var (
		keys           []string = []string{"time", "schemas"} // InfluxDB related annotations keys.
		mustCollect    func(trace *Trace)
		mustCollectAll func(trace *Trace)
		tracesMap      map[ID]*Trace = make(map[ID]*Trace, 0) // Trace ID -> Trace.
	)

	mustCollect = func(trace *Trace) {
		if err := store.Collect(trace.Span.ID, trace.Span.Annotations...); err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
	}
	mustCollectAll = func(trace *Trace) {
		for _, sub := range trace.Sub {
			mustCollect(sub)
			mustCollectAll(sub)
		}
	}
	for _, trace := range traces {
		tracesMap[trace.ID.Trace] = trace
	}

	// InfluxDBStore.Collect(...) tests.
	for _, trace := range traces {
		mustCollect(trace)
		mustCollectAll(trace)
	}

	mustBeEqual := func(gotTrace, want *Trace) {
		removeInfluxDBAnnotations(gotTrace, keys)
		sortAnnotations(*gotTrace, *want)
		if !reflect.DeepEqual(gotTrace, want) {
			t.Fatalf("got: %v, want: %v", gotTrace, want)
		}
	}

	// InfluxDBStore.Trace(...) tests.
	for _, trace := range traces {
		gotTrace, err := store.Trace(trace.ID.Trace)
		if err != nil {
			t.Fatalf("unexpected error: %+v", err)
		}
		if t == nil {
			t.Fatalf("expected a trace, got nil")
		}
		want, found := tracesMap[gotTrace.ID.Trace]
		if !found {
			t.Fatal("trace not found")
		}
		mustBeEqual(gotTrace, want)
	}

	// InfluxDBStore.Traces(...) tests.
	gotTraces, err := store.Traces()
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if len(gotTraces) != len(traces) {
		t.Fatalf("unexpected quantity of traces, got: %v, want: %v", len(gotTraces), len(traces))
	}
	for _, gotTrace := range gotTraces {
		want, found := tracesMap[gotTrace.ID.Trace]
		if !found {
			t.Fatal("trace not found")
		}
		mustBeEqual(gotTrace, want)
	}
}

func newStore(t *testing.T) *InfluxDBStore {
	conf, err := influxDBServer.NewDemoConfig()
	if err != nil {
		t.Fatalf("failed to create influxdb config, error: %v", err)
	}
	conf.HTTPD.AuthEnabled = true
	user := InfluxDBAdminUser{Username: "demo", Password: "demo"}
	defaultRP := InfluxDBRetentionPolicy{Name: "one_hour_only", Duration: "1h"}
	store, err := NewInfluxDBStore(InfluxDBStoreConfig{
		AdminUser: user,
		BuildInfo: &influxDBServer.BuildInfo{},
		DefaultRP: defaultRP,
		Mode:      testMode,
		Server:    conf,
	})
	if err != nil {
		t.Fatalf("failed to create influxdb store, error: %v", err)
	}
	return store
}

// removeInfluxDBAnnotations removes annotations from `root` and it's subtraces; only those annotations that have as key present on `keys` will be removed.
func removeInfluxDBAnnotations(root *Trace, keys []string) {
	var (
		walk     func(root *Trace)
		removeFn func(trace *Trace, keys []string)
	)
	removeFn = func(trace *Trace, keys []string) {
		for i := len(trace.Annotations) - 1; i >= 0; i-- {
			for _, k := range keys {
				if trace.Annotations[i].Key == k {
					trace.Annotations = append(trace.Annotations[:i], trace.Annotations[i+1:]...)
					break
				}
			}
		}
	}
	walk = func(root *Trace) {
		removeFn(root, keys)
		for _, sub := range root.Sub {
			removeFn(sub, keys)
			walk(sub)
		}
	}
	walk(root)
}

// sortSchemas sorts schemas(strings) within `s` which is
// a set of schemas separated by `schemasFieldSeparator`.
func sortSchemas(s string) string {
	schemas := strings.Split(s, schemasFieldSeparator)
	sort.Sort(bySchemaText(schemas))
	return strings.Join(schemas, schemasFieldSeparator)
}

func sortAnnotations(traces ...Trace) {
	var walk func(t *Trace)
	walk = func(t *Trace) {
		sort.Sort(annotations(t.Span.Annotations))
		for _, s := range t.Sub {
			sort.Sort(annotations(s.Span.Annotations))
			walk(s)
		}
	}
	for _, t := range traces {
		walk(&t)
	}
}

type bySchemaText []string

func (bs bySchemaText) Len() int           { return len(bs) }
func (bs bySchemaText) Swap(i, j int)      { bs[i], bs[j] = bs[j], bs[i] }
func (bs bySchemaText) Less(i, j int) bool { return bs[i] < bs[j] }
