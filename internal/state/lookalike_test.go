package state

import "testing"

func TestRiskyKeyLookalikes(t *testing.T) {
	for key, want := range map[string]bool{
		"pаssword":    true, // Cyrillic а
		"DB_PASSWΟRD": true, // Greek Ο
		"ｔｏｋｅｎ":       true, // fullwidth
		"𝐬𝐞𝐜𝐫𝐞𝐭":      true, // mathematical bold
		"𝟙_api_key":   true,
		"passаword":   true, // dropping the letter still matches
		"Größe":       false,
		"région":      false,
		"bucket":      false,
	} {
		if got := RiskyKey(key); got != want {
			t.Errorf("RiskyKey(%q) = %v", key, got)
		}
	}
}
