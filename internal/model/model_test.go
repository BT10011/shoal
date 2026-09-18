package model

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func obs(f Field, value, source string, conf float32, at time.Time, ttl time.Duration) Observation {
	return Observation{
		DeviceKey:  "aa:bb:cc:dd:ee:ff",
		Field:      f,
		Value:      value,
		Source:     source,
		Method:     source + " says " + value,
		Confidence: conf,
		At:         at,
		TTL:        ttl,
	}
}

func TestResolvedPrefersHighestConfidence(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldHostname, "from-rdns", "rdns", 0.9, t0, 0))
	d.Add(obs(FieldHostname, "from-mdns", "mdns", 0.7, t0.Add(time.Second), 0))

	got, ok := d.ResolvedAt(FieldHostname, t0.Add(2*time.Second))
	if !ok || got.Value != "from-rdns" {
		t.Fatalf("got %q ok=%v, want from-rdns", got.Value, ok)
	}
}

func TestResolvedBreaksTiesBySourcePriority(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldHostname, "from-nbns", "nbns", 0.8, t0.Add(time.Second), 0))
	d.Add(obs(FieldHostname, "from-mdns", "mdns", 0.8, t0, 0))

	got, _ := d.ResolvedAt(FieldHostname, t0.Add(2*time.Second))
	if got.Value != "from-mdns" {
		t.Fatalf("got %q, want from-mdns (higher source priority despite being older)", got.Value)
	}
}

func TestResolvedBreaksRemainingTiesByNewest(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldService, "_old._tcp", "mdns", 0.8, t0, 0))
	d.Add(obs(FieldService, "_new._tcp", "mdns", 0.8, t0.Add(time.Second), 0))

	got, _ := d.ResolvedAt(FieldService, t0.Add(2*time.Second))
	if got.Value != "_new._tcp" {
		t.Fatalf("got %q, want _new._tcp", got.Value)
	}
}

func TestResolvedSkipsExpired(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldHostname, "short-lived", "mdns", 0.9, t0, time.Minute))
	d.Add(obs(FieldHostname, "durable", "rdns", 0.5, t0, 0))

	if got, _ := d.ResolvedAt(FieldHostname, t0.Add(30*time.Second)); got.Value != "short-lived" {
		t.Fatalf("before expiry got %q, want short-lived", got.Value)
	}
	if got, _ := d.ResolvedAt(FieldHostname, t0.Add(time.Minute)); got.Value != "durable" {
		t.Fatalf("at expiry got %q, want durable", got.Value)
	}
}

func TestResolvedNothingLive(t *testing.T) {
	d := NewDevice("k", t0)
	if _, ok := d.ResolvedAt(FieldHostname, t0); ok {
		t.Fatal("expected no value for empty field")
	}
	d.Add(obs(FieldHostname, "x", "mdns", 0.9, t0, time.Second))
	if _, ok := d.ResolvedAt(FieldHostname, t0.Add(time.Hour)); ok {
		t.Fatal("expected no value once all observations expired")
	}
}

func TestExpired(t *testing.T) {
	o := obs(FieldIP, "1", "arp", 1, t0, time.Minute)
	if o.Expired(t0.Add(59 * time.Second)) {
		t.Fatal("expired too early")
	}
	if !o.Expired(t0.Add(time.Minute)) {
		t.Fatal("should expire exactly at At+TTL")
	}
	if (Observation{At: t0}).Expired(t0.Add(1000 * time.Hour)) {
		t.Fatal("zero TTL must never expire")
	}
}

func TestAddSingleValuedReplacesPerSource(t *testing.T) {
	d := NewDevice("k", t0)
	if !d.Add(obs(FieldLatency, "1ms", "icmp", 1, t0, 0)) {
		t.Fatal("first add should report change")
	}
	if d.Add(obs(FieldLatency, "1ms", "icmp", 1, t0.Add(time.Second), 0)) {
		t.Fatal("same value from same source should not report change")
	}
	if !d.Add(obs(FieldLatency, "2ms", "icmp", 1, t0.Add(2*time.Second), 0)) {
		t.Fatal("new value from same source should report change")
	}
	if n := len(d.Facts[FieldLatency]); n != 1 {
		t.Fatalf("single-valued field kept %d observations from one source, want 1", n)
	}
	if got := d.Facts[FieldLatency][0]; got.Value != "2ms" || !got.At.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("kept %+v, want latest", got)
	}
}

func TestAddSingleValuedKeepsOnePerSource(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldHostname, "a", "mdns", 0.9, t0, 0))
	d.Add(obs(FieldHostname, "b", "rdns", 0.9, t0, 0))
	if n := len(d.Facts[FieldHostname]); n != 2 {
		t.Fatalf("got %d observations, want one per source", n)
	}
}

func TestAddMultiValuedDedupesPerSourceAndValue(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldService, "_ssh._tcp", "mdns", 1, t0, 0))
	d.Add(obs(FieldService, "_http._tcp", "mdns", 1, t0, 0))
	if d.Add(obs(FieldService, "_ssh._tcp", "mdns", 1, t0.Add(time.Second), 0)) {
		t.Fatal("repeat of an existing service should not report change")
	}
	if n := len(d.Facts[FieldService]); n != 2 {
		t.Fatalf("got %d services, want 2", n)
	}
	if got := d.Values(FieldService, t0.Add(2*time.Second)); len(got) != 2 {
		t.Fatalf("Values = %v, want both services", got)
	}
}

func TestAddTracksFirstAndLastSeen(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldIP, "1", "arp", 1, t0.Add(time.Minute), 0))
	d.Add(obs(FieldIP, "1", "neigh", 1, t0.Add(-time.Minute), 0))
	if !d.FirstSeen.Equal(t0.Add(-time.Minute)) {
		t.Fatalf("FirstSeen = %v", d.FirstSeen)
	}
	if !d.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("LastSeen = %v", d.LastSeen)
	}
}

func TestConflicting(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldHostname, "printer", "mdns", 0.9, t0, 0))
	if d.Conflicting(FieldHostname, t0) {
		t.Fatal("one value is not a conflict")
	}
	d.Add(obs(FieldHostname, "printer", "rdns", 0.7, t0, 0))
	if d.Conflicting(FieldHostname, t0) {
		t.Fatal("two sources agreeing is not a conflict")
	}
	d.Add(obs(FieldHostname, "PRINTER-01", "nbns", 0.6, t0, time.Minute))
	if !d.Conflicting(FieldHostname, t0) {
		t.Fatal("disagreeing sources should conflict")
	}
	if d.Conflicting(FieldHostname, t0.Add(time.Hour)) {
		t.Fatal("conflict should clear once the disagreeing observation expires")
	}

	d.Add(obs(FieldFlag, "a", "store", 1, t0, 0))
	d.Add(obs(FieldFlag, "b", "store", 1, t0, 0))
	if d.Conflicting(FieldFlag, t0) {
		t.Fatal("multi-valued fields never conflict")
	}
}

func TestLiveOrderAndFields(t *testing.T) {
	d := NewDevice("k", t0)
	d.Add(obs(FieldVendor, "Apple", "oui", 0.8, t0, 0))
	d.Add(obs(FieldHostname, "x", "mdns", 0.9, t0, 0))
	d.Add(obs(FieldHostname, "y", "rdns", 0.95, t0, 0))

	live := d.Live(FieldHostname, t0)
	if len(live) != 2 || live[0].Value != "y" || live[1].Value != "x" {
		t.Fatalf("Live order wrong: %+v", live)
	}
	fields := d.Fields()
	if len(fields) != 2 || fields[0] != FieldHostname || fields[1] != FieldVendor {
		t.Fatalf("Fields = %v", fields)
	}
}

func TestSnapshotIsIndependent(t *testing.T) {
	d := NewDevice("k", t0)
	o := obs(FieldMAC, "aa", "arp", 1, t0, 0)
	o.Raw = []byte{1, 2, 3}
	d.Add(o)

	snap := d.Snapshot()
	d.Add(obs(FieldHostname, "later", "mdns", 1, t0, 0))
	d.Facts[FieldMAC][0].Raw[0] = 99

	if _, ok := snap.Facts[FieldHostname]; ok {
		t.Fatal("snapshot saw a later addition")
	}
	if snap.Facts[FieldMAC][0].Raw[0] != 1 {
		t.Fatal("snapshot shares Raw bytes with the original")
	}
	if got, _ := snap.ResolvedAt(FieldMAC, t0); got.Value != "aa" {
		t.Fatalf("snapshot Resolved = %q", got.Value)
	}
}

func TestLastContactCountsOnlyDirectSources(t *testing.T) {
	d := NewDevice("k", t0)
	if _, _, ok := d.LastContact(); ok {
		t.Fatal("empty device has no contact")
	}
	d.Add(obs(FieldVendor, "Acme", "oui", 0.9, t0.Add(30*time.Second), 0))
	d.Add(obs(FieldHostname, "nas.lan", "rdns", 0.7, t0.Add(40*time.Second), 0))
	d.Add(obs(FieldFlag, "duplicate-ip", "store", 1, t0.Add(50*time.Second), 0))
	if _, _, ok := d.LastContact(); ok {
		t.Fatal("a table, a resolver and the store have not heard from the device")
	}
	d.Add(obs(FieldIP, "10.0.0.1", "arp", 1, t0, 0))
	d.Add(obs(FieldLatency, "1ms", "icmp", 1, t0.Add(10*time.Second), time.Second)) // expired, still counts
	at, source, ok := d.LastContact()
	if !ok || source != "icmp" || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("contact = %v %q %v", at, source, ok)
	}
	if !Direct("arp") || Direct("oui") || Direct("unknown") {
		t.Fatal("Direct classification wrong")
	}
	d.Add(obs(FieldHostname, "h", "mdns", 0.9, t0.Add(10*time.Second), 0))
	d.Add(obs(FieldIP, "10.0.0.1", "arp", 1, t0.Add(10*time.Second), 0))
	if _, source, _ := d.LastContact(); source != "arp" {
		t.Fatalf("same instant should be settled by source priority, got %q", source)
	}
}

func TestHistoryIsContactButNeverAConflict(t *testing.T) {
	d := NewDevice("k", t0)
	past := t0.Add(-48 * time.Hour)
	d.Add(obs(FieldHostname, "old-name.local", SourceHistory, 0.5, past, 0))
	d.Add(obs(FieldHostname, "new-name.local", "mdns", 0.9, t0, 0))
	if d.Conflicting(FieldHostname, t0) {
		t.Fatal("a remembered name differing from today's is a change, not a conflict")
	}
	if got, _ := d.ResolvedAt(FieldHostname, t0); got.Value != "new-name.local" {
		t.Fatalf("today's value must win: %q", got.Value)
	}
	if vs := d.Values(FieldHostname, t0); len(vs) != 2 {
		t.Fatalf("the remembered value is still a value to show: %v", vs)
	}
	d.Add(obs(FieldHostname, "nas.lan", "rdns", 0.7, t0, 0))
	if !d.Conflicting(FieldHostname, t0) {
		t.Fatal("two current sources disagreeing is still a conflict")
	}

	ghost := NewDevice("g", past)
	ghost.Add(obs(FieldMAC, "aa:bb:cc:dd:ee:ff", SourceHistory, 0.5, past, 0))
	at, source, ok := ghost.LastContact()
	if !ok || source != SourceHistory || !at.Equal(past) {
		t.Fatalf("history counts as contact at the time it recalls: %v %q %v", at, source, ok)
	}
	if !Historical(SourceHistory) || Historical("arp") {
		t.Fatal("Historical classification wrong")
	}
	if SourcePriority(SourceHistory) >= SourcePriority("classify") {
		t.Fatal("history must lose every tie")
	}
}
