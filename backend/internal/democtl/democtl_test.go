package democtl

import "testing"

func TestIsCleanupOnlyFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "windows cleanup race after passing tests",
			out:  "ok  \tgithub.com/patchpilot/demo_app\t2.0s\ngo: unlinkat C:\\Temp\\demo_app.test.exe: The process cannot access the file because it is being used by another process.\n",
			want: true,
		},
		{
			name: "real test failure alongside cleanup warning",
			out:  "--- FAIL: TestCheckout (0.00s)\nFAIL\ngo: unlinkat ...: being used by another process\n",
			want: false,
		},
		{
			name: "compile failure",
			out:  "./main.go:10:2: undefined: foo\nbeing used by another process",
			want: false,
		},
		{
			name: "ordinary failure without cleanup warning",
			out:  "--- FAIL: TestReport (0.10s)\nFAIL",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isCleanupOnlyFailure(c.out); got != c.want {
				t.Fatalf("isCleanupOnlyFailure(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}
