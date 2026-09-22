package webhook

import "testing"

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"id":1,"type":"report.created"}`)
	sig := Sign("s3cr3t", body)

	if !Verify("s3cr3t", body, sig) {
		t.Fatal("a signature produced by Sign must verify")
	}
	if Verify("wrong", body, sig) {
		t.Error("a different secret must not verify")
	}
	if Verify("s3cr3t", []byte(`{"id":2}`), sig) {
		t.Error("a tampered body must not verify")
	}
	if Verify("s3cr3t", body, "") {
		t.Error("an empty header must not verify")
	}
}
