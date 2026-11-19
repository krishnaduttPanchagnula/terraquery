package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLeavesSkipsSecrets(t *testing.T) {
	v := map[string]any{
		"password": "hunter2",
		"role_arn": "arn:aws:iam::111122223333:role/x",
		"nested":   []any{map[string]any{"api_key": "k", "host": "h.example.com"}},
	}
	got := map[string]string{}
	Leaves(v, "", func(p, s string) { got[p] = s })
	if _, ok := got["password"]; ok {
		t.Fatal("password leaked")
	}
	if _, ok := got["nested[0].api_key"]; ok {
		t.Fatal("api_key leaked")
	}
	if got["role_arn"] == "" || got["nested[0].host"] == "" {
		t.Fatalf("expected arn and host leaves, got %v", got)
	}
}

func TestSanitized(t *testing.T) {
	raw := []byte(`{"version":4,"serial":1,"lineage":"x","outputs":{
		"leak":{"value":"topsecret","sensitive":true},
		"ok":{"value":"h.example.com","sensitive":false}},
		"resources":[{"mode":"managed","type":"aws_x","name":"y","instances":[{"attributes":{
			"password":"hunter2","role_arn":"arn:aws:iam::1:role/x",
			"nested":[{"api_key":"k","host":"h.example.com"}]}}]}]}`)
	f, err := Parse(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := Sanitized(f)
	if _, ok := c.Outputs["leak"]; ok {
		t.Fatal("sensitive output not removed")
	}
	b, _ := json.Marshal(c)
	s := string(b)
	for _, secret := range []string{"hunter2", "topsecret", "api_key", "password"} {
		if strings.Contains(s, secret) {
			t.Fatalf("secret %q survived sanitization: %s", secret, s)
		}
	}
	if !strings.Contains(s, "arn:aws:iam::1:role/x") || !strings.Contains(s, "h.example.com") {
		t.Fatalf("non-secret values were lost: %s", s)
	}
}
