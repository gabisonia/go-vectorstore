package sqlite

import (
	"net/url"
	"testing"
)

func TestFileURIHasNoAuthority(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/tmp/literal?#% 雪.db", "file:///tmp/literal%3F%23%25%20%E9%9B%AA.db"},
		{"C:/Users/test/literal#% 雪.db", "file:///C:/Users/test/literal%23%25%20%E9%9B%AA.db"},
		{"/C:/already/slashed.db", "file:///C:/already/slashed.db"},
		{"//server/share/literal#%.db", "file:////server/share/literal%23%25.db"},
		{"/tmp/literal\\name.db", "file:///tmp/literal%5Cname.db"},
	} {
		got := sqliteFileURI(tc.path)
		if got != tc.want {
			t.Errorf("path %q: got %q want %q", tc.path, got, tc.want)
		}
		parsed, err := url.Parse(got)
		if err != nil || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Errorf("filename became authority/query/fragment: %#v %v", parsed, err)
		}
	}
}
