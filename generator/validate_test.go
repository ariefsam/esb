package generator

import "testing"

func TestValidateSnakeName(t *testing.T) {
	valid := []string{"order", "bank_account", "place_order", "orders_by_buyer", "a1", "user_v2"}
	for _, s := range valid {
		if err := validateSnakeName("aggregate name", s); err != nil {
			t.Errorf("validateSnakeName(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"", "Order", "bankAccount", "_order", "order_", "order__x", "1order", "order-item", "órder"}
	for _, s := range invalid {
		if err := validateSnakeName("aggregate name", s); err == nil {
			t.Errorf("validateSnakeName(%q) = nil, want error", s)
		}
	}
}

func TestValidatePascalName(t *testing.T) {
	valid := []string{"OrderPlaced", "Placed", "Order2", "X"}
	for _, s := range valid {
		if err := validatePascalName("event name", s); err != nil {
			t.Errorf("validatePascalName(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"", "orderPlaced", "order_placed", "2Order", "Order Placed", "Order-Placed"}
	for _, s := range invalid {
		if err := validatePascalName("event name", s); err == nil {
			t.Errorf("validatePascalName(%q) = nil, want error", s)
		}
	}
}

// TestAdd_RejectsInvalidNamesWithoutPanic is the regression for the assessment
// finding that Add* panicked on empty/invalid names via name[:1]. These must
// return a clean error instead of panicking.
func TestAdd_RejectsInvalidNamesWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := InitProject("example.com/shop", dir); err != nil {
		t.Fatalf("InitProject() error = %v", err)
	}

	if err := AddAggregate(""); err == nil {
		t.Error("AddAggregate(\"\") = nil, want error")
	}
	if err := AddHandler("", "order"); err == nil {
		t.Error("AddHandler(\"\", order) = nil, want error")
	}
	if err := AddEvent("order", "", nil); err == nil {
		t.Error("AddEvent(order, \"\") = nil, want error")
	}
	if err := AddQuery("", "order"); err == nil {
		t.Error("AddQuery(\"\", order) = nil, want error")
	}
	if err := AddProjection("", []string{"order"}); err == nil {
		t.Error("AddProjection(\"\", ...) = nil, want error")
	}
	if err := AddProjection("report", nil); err == nil {
		t.Error("AddProjection(report, nil) = nil, want error")
	}
	if _, err := ParseFields([]string{"BadField:int64"}); err == nil {
		t.Error("ParseFields(BadField:int64) = nil, want error")
	}
}
