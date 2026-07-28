package invite

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if err := ValidateToken(token); err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	hash := HashToken(token)
	if !MatchToken(hash, token) {
		t.Fatal("MatchToken rejected matching token")
	}
	other, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if MatchToken(hash, other) {
		t.Fatal("MatchToken accepted another token")
	}
}

func TestValidateTokenRejectsShortValue(t *testing.T) {
	t.Parallel()
	if err := ValidateToken("short"); err == nil {
		t.Fatal("ValidateToken accepted short value")
	}
}
