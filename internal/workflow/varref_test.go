package workflow

import (
	"reflect"
	"testing"
)

func TestExtractRefs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []VarRef
	}{
		{
			name: "no refs",
			in:   "plain text",
			want: nil,
		},
		{
			name: "bare-dollar is literal",
			in:   "echo $FOO",
			want: nil, // single-segment $FOO has no dot → literal
		},
		{
			name: "node ref with .output",
			in:   "Read $plan.output and act",
			want: []VarRef{{Raw: "$plan.output", Category: CategoryNode, Name: "plan", Path: []string{}}},
		},
		{
			name: "node ref with json path",
			in:   "use $plan.output.tasks.first.title",
			want: []VarRef{{Raw: "$plan.output.tasks.first.title", Category: CategoryNode, Name: "plan", Path: []string{"tasks", "first", "title"}}},
		},
		{
			name: "env ref",
			in:   "connect to $ENV.NATS_URL",
			want: []VarRef{{Raw: "$ENV.NATS_URL", Category: CategoryEnv, Name: "NATS_URL"}},
		},
		{
			name: "workflow ref",
			in:   "scope=$WORKFLOW.scope_id",
			want: []VarRef{{Raw: "$WORKFLOW.scope_id", Category: CategoryStatic, Name: "scope_id"}},
		},
		{
			name: "escaped is literal",
			in:   `keep \$plan.output literal`,
			want: nil,
		},
		{
			name: "mixed",
			in:   "Read $plan.output via $ENV.NATS_URL scoped to $WORKFLOW.name",
			want: []VarRef{
				{Raw: "$plan.output", Category: CategoryNode, Name: "plan", Path: []string{}},
				{Raw: "$ENV.NATS_URL", Category: CategoryEnv, Name: "NATS_URL"},
				{Raw: "$WORKFLOW.name", Category: CategoryStatic, Name: "name"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractRefs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractRefs(%q):\n got=%#v\nwant=%#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNodeRefs_filtersNonNode(t *testing.T) {
	in := "use $plan.output but also $ENV.X and $WORKFLOW.y"
	got := NodeRefs(in)
	if len(got) != 1 {
		t.Fatalf("want 1 node ref, got %d: %#v", len(got), got)
	}
	if got[0].Name != "plan" {
		t.Errorf("want name=plan, got %q", got[0].Name)
	}
}
