package ojson

import "testing"

func TestRoundTripKeepsOrderAndFormat(t *testing.T) {
	in := `{
  "zeta": 1,
  "alpha": {
    "b": [
      true,
      null,
      "x<y>&"
    ],
    "a": 1.50
  },
  "empty": {},
  "list": []
}
`
	obj, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed the document:\n%s", out)
	}
}

func TestSetAppendsAndReplaces(t *testing.T) {
	obj, _ := Parse([]byte(`{"a": 1}`))
	obj.Set("b", "x")
	obj.Set("a", "y")
	obj.Delete("missing")
	out, _ := Marshal(obj)
	want := "{\n  \"a\": \"y\",\n  \"b\": \"x\"\n}\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestRejectsNonObject(t *testing.T) {
	if _, err := Parse([]byte(`[1]`)); err == nil {
		t.Fatal("expected an error for a top-level array")
	}
}
