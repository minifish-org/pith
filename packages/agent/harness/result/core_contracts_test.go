package result

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestResultConstructorsAndJSON(t *testing.T) {
	ok := Ok[string, error]("value")
	if !ok.IsOk() || ok.IsErr() {
		t.Fatal("expected ok result")
	}
	encoded, err := json.Marshal(ok)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ok"] != true || decoded["value"] != "value" {
		t.Fatalf("encoded ok = %s", encoded)
	}
	if _, present := decoded["error"]; present {
		t.Fatalf("ok result must not carry an error key: %s", encoded)
	}

	failure := Err[string, error](errors.New("boom"))
	encoded, err = json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	decoded = map[string]any{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ok"] != false {
		t.Fatalf("encoded err = %s", encoded)
	}
	if _, present := decoded["value"]; present {
		t.Fatalf("error result must not carry a value key: %s", encoded)
	}
}

func TestTaggedErrorJSONAndIs(t *testing.T) {
	busy := &LaneBusy{Lane: "main", OperationId: "op", OperationKind: "run", Message: "busy"}
	if busy.Tag() != "LaneBusy" || busy.Error() != "busy" {
		t.Fatalf("tag/message = %q/%q", busy.Tag(), busy.Error())
	}
	payload := busy.ToJSON()
	if payload["_tag"] != "LaneBusy" || payload["lane"] != "main" || payload["operationId"] != "op" || payload["message"] != "busy" {
		t.Fatalf("payload = %#v", payload)
	}
	factory := TaggedError("LaneBusy")
	if !factory.Is(busy) {
		t.Fatal("factory should recognize its family")
	}
	if factory.Is(&Closed{Message: "x"}) {
		t.Fatal("factory must not recognize another family")
	}
	created, createErr := factory.New(map[string]any{"message": "made"})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if created.Tag() != "LaneBusy" || created.Error() != "made" {
		t.Fatalf("created = %+v", created)
	}
}

func TestMatchError(t *testing.T) {
	value := MatchError[string](&Closed{Message: "x"}, ErrorMatchers[string]{
		"Closed": func(error) string { return "closed" },
	})
	if value != "closed" {
		t.Fatalf("match = %q", value)
	}
}
