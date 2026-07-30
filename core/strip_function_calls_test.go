package core

import "testing"

func TestStripFunctionCalls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no_tags_untouched",
			in:   "just a normal reply about function call design",
			want: "just a normal reply about function call design",
		},
		{
			name: "paired_block_removed",
			in:   `before<function_calls><invoke name="Bash"><parameter name="command">ls</parameter></invoke></function_calls>after`,
			want: "beforeafter",
		},
		{
			name: "namespaced_paired_block_removed",
			in:   `x<function_calls><invoke name="Read"></invoke></function_calls>y`,
			want: "xy",
		},
		{
			name: "multiline_block_removed",
			in:   "line1\n<function_calls>\n<invoke name=\"Edit\">\n<parameter name=\"p\">v</parameter>\n</invoke>\n</function_calls>\nline2",
			want: "line1\n\nline2",
		},
		{
			name: "unclosed_fragment_left_alone",
			in:   `partial <function_calls><invoke name="Bash"`,
			want: `partial <function_calls><invoke name="Bash"`,
		},
		{
			name: "two_blocks_removed",
			in:   `a<function_calls><invoke name="A"></invoke></function_calls>b<function_calls><invoke name="B"></invoke></function_calls>c`,
			want: "abc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripFunctionCalls(tc.in)
			if got != tc.want {
				t.Errorf("StripFunctionCalls(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
