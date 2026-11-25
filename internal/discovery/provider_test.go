package discovery

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestVerifyUsesNamedProviderAndRejectsUnknown(t *testing.T) {
	s := newService(&fakeRunner{})
	p := pin()
	p.Provider = "gcp"
	if _, err := s.Verify(context.Background(), p, nil); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unknown provider: %v", err)
	}
	if id := verify(t, newService(&fakeRunner{}), pin()); id.Provider != "aws" {
		t.Fatalf("provider = %q", id.Provider)
	}
}

func TestPinDiffersOnProvider(t *testing.T) {
	a, b := pin(), pin()
	b.Provider = "gcp"
	if a.differs(b) == "" {
		t.Fatal("a verification made for one provider must not serve another")
	}
}

func TestAWSListArgs(t *testing.T) {
	typ, _ := TypeByName("aws_vpc")
	got := awsProvider{}.ListArgs(typ, "eu-west-1", 100, "tok")
	want := []string{"ec2", "describe-vpcs", "--output", "json", "--region", "eu-west-1", "--max-items", "100", "--starting-token", "tok"}
	if !slices.Equal(got, want) {
		t.Fatalf("args = %v", got)
	}
}
