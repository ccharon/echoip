package http

import "testing"

func TestCoordinates(t *testing.T) {
	tests := []struct {
		name string
		lat  float64
		lon  float64
		want string
	}{
		{"both", 63.416667, 10.416667, "63.416667,10.416667"},
		{"southern and western", -29, -82.3925, "-29.000000,-82.392500"},
		{"latitude alone", 63.416667, 0, "63.416667,0.000000"},
		{"longitude alone", 0, 10.416667, "0.000000,10.416667"},
		// A lookup that found nothing leaves both at zero, which is a place in
		// the Gulf of Guinea rather than an answer.
		{"neither", 0, 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Response{Latitude: tt.lat, Longitude: tt.lon}
			if got := r.Coordinates(); got != tt.want {
				t.Errorf("Coordinates() = %q, want %q", got, tt.want)
			}
		})
	}
}
