package tag

import "testing"

func TestNormalize(t *testing.T) {
	ok := map[string]string{
		"#Tm_TV_322280_1_6": "tm_tv_322280_1_6",
		"tm_m_603":          "tm_m_603",
		"  #a  ":            "a",
	}
	for in, want := range ok {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "#", "tm-tv", "tm tv", "tm_tv_*", "tm_tv_é", "tм_tv", "a/b", "x\ny"}
	for _, in := range bad {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) accepted", in)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Normalize(string(long)); err != ErrTooLong {
		t.Errorf("65 characters: %v", err)
	}
}

func TestParsePattern(t *testing.T) {
	p, err := ParsePattern("#tm_tv_322280_1_6*")
	if err != nil || !p.Wild || p.Prefix != "tm_tv_322280_1_6" || p.Text != "tm_tv_322280_1_6*" {
		t.Fatalf("got %+v, %v", p, err)
	}
	if p.UpperBound() != "tm_tv_322280_1_7" {
		t.Errorf("upper bound %q", p.UpperBound())
	}
	p, err = ParsePattern("#tm_tv_322280_1_6_**_fa")
	if err != nil || p.Text != "tm_tv_322280_1_6_*_fa" {
		t.Errorf("collapse: %+v, %v", p, err)
	}
	p, err = ParsePattern("#tm_m_603")
	if err != nil || p.Wild {
		t.Errorf("exact: %+v, %v", p, err)
	}
	for _, in := range []string{"*", "#*", "#tm_*", "#tm_tv*", "#ab*cdefgh", "#tm_tv_1?", "#tm_[a]_1"} {
		if _, err := ParsePattern(in); err == nil {
			t.Errorf("ParsePattern(%q) accepted", in)
		}
	}
}
