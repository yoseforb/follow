//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	valkeygo "github.com/valkey-io/valkey-go"
	"github.com/yoseforb/follow-pkg/valkey"
)

// seedRoute describes a single route to create.
type seedRoute struct {
	Address       string
	LocationName  string
	StartPoint    string
	EndPoint      string
	Description   string
	WaypointCount int
}

// seedRoutes is the full list of 60 routes to seed.
var seedRoutes = []seedRoute{
	// Ichilov Hospital (15 routes)
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Cardiology, Building B, Floor 3",
		WaypointCount: 3,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Underground Parking P2",
		EndPoint:      "Emergency Room",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Radiology, Building A, Floor -1",
		WaypointCount: 3,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Gate 3 (Weizmann St)",
		EndPoint:      "Maternity Ward, Floor 5",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Underground Parking P1",
		EndPoint:      "Orthopedics, Building C, Floor 2",
		WaypointCount: 3,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Emergency Room Entrance",
		EndPoint:      "ICU, Building A, Floor 4",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Cafeteria",
		EndPoint:      "Oncology Day Care, Building D",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Blood Tests Lab, Floor -1",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Gate 2 (Dubnov St)",
		EndPoint:      "Neurology, Building B, Floor 6",
		WaypointCount: 3,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Underground Parking P1",
		EndPoint:      "Eye Clinic, Building E",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Children's ER, Building A",
		WaypointCount: 3,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Gate 3 (Weizmann St)",
		EndPoint:      "Dialysis Unit, Floor 2",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Visitor Parking",
		EndPoint:      "Physical Therapy Center",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Pharmacy, Ground Floor",
		WaypointCount: 2,
	},
	{
		Address:       "6 Weizmann St, Tel Aviv",
		LocationName:  "Ichilov Hospital",
		StartPoint:    "Emergency Room Entrance",
		EndPoint:      "Trauma Center, Building A",
		WaypointCount: 3,
	},

	// Sheba Medical Center (10 routes)
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Heart Center, Building 44",
		WaypointCount: 3,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Parking Lot B",
		EndPoint:      "Children's Hospital",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Bus Stop Entrance",
		EndPoint:      "Rehabilitation Center",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Emergency Entrance",
		EndPoint:      "Neurology, Building 12",
		WaypointCount: 3,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Oncology Center",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Parking Lot A",
		EndPoint:      "Dialysis Unit, Building 8",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "South Gate",
		EndPoint:      "Research Tower",
		WaypointCount: 3,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "MRI Center, Building 17",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Emergency Entrance",
		EndPoint:      "Burn Unit",
		WaypointCount: 2,
	},
	{
		Address:       "2 Sheba Rd, Ramat Gan",
		LocationName:  "Sheba Medical Center",
		StartPoint:    "Parking Lot C",
		EndPoint:      "Psychiatric Wing",
		WaypointCount: 2,
	},

	// Hadassah Ein Kerem (10 routes)
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Main Entrance",
		EndPoint:      "Surgical Ward, Floor 5",
		WaypointCount: 3,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Underground Parking",
		EndPoint:      "Round Building, Oncology",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Emergency Entrance",
		EndPoint:      "Mother & Baby Center",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Visitor Entrance",
		EndPoint:      "Chagall Windows Synagogue",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Main Entrance",
		EndPoint:      "Cardiology, Floor 3",
		WaypointCount: 3,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Underground Parking",
		EndPoint:      "Eye Institute",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Emergency Entrance",
		EndPoint:      "Pediatric ER",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Gate B",
		EndPoint:      "Rehabilitation Center",
		WaypointCount: 3,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Main Entrance",
		EndPoint:      "Bone Marrow Transplant, Floor 7",
		WaypointCount: 2,
	},
	{
		Address:       "Kalman Ya'akov Man St, Jerusalem",
		LocationName:  "Hadassah Ein Kerem",
		StartPoint:    "Visitor Entrance",
		EndPoint:      "Chapel & Garden",
		WaypointCount: 2,
	},

	// Rambam Medical Center (10 routes)
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Underground Emergency Hospital",
		WaypointCount: 3,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Parking Structure",
		EndPoint:      "Pediatrics, Building B",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "South Entrance",
		EndPoint:      "Orthopedics, Floor 3",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Cardiology Tower, Floor 7",
		WaypointCount: 3,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "North Entrance",
		EndPoint:      "Neurosurgery, Building A",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Parking Structure",
		EndPoint:      "Women's Health Center",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Sammy Ofer Fortified Wing",
		WaypointCount: 3,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Emergency Entrance",
		EndPoint:      "Trauma Unit",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "South Entrance",
		EndPoint:      "Dermatology Clinic",
		WaypointCount: 2,
	},
	{
		Address:       "8 HaAliya HaShniya St, Haifa",
		LocationName:  "Rambam Medical Center",
		StartPoint:    "Main Gate",
		EndPoint:      "Meyer Children's Hospital",
		WaypointCount: 3,
	},

	// Independent routes (15 routes)
	{
		Address:       "Dizengoff Center, Tel Aviv",
		LocationName:  "Dizengoff Center",
		StartPoint:    "Street Level Entrance",
		EndPoint:      "Cinema, Floor 3",
		WaypointCount: 2,
	},
	{
		Address:       "Ben Gurion Airport, Terminal 3",
		LocationName:  "Ben Gurion Airport",
		StartPoint:    "Arrivals Hall",
		EndPoint:      "Gate C12",
		WaypointCount: 3,
	},
	{
		Address:       "Hebrew University, Mt Scopus, Jerusalem",
		LocationName:  "Hebrew University",
		StartPoint:    "Main Gate",
		EndPoint:      "Faculty of Law, Building 4",
		WaypointCount: 2,
	},
	{
		Address:       "Azrieli Mall, Derech Menachem Begin, Tel Aviv",
		LocationName:  "Azrieli Center",
		StartPoint:    "Parking Level -3",
		EndPoint:      "Food Court, Round Tower",
		WaypointCount: 2,
	},
	{
		Address:       "Central Bus Station, Jerusalem",
		LocationName:  "Jerusalem CBS",
		StartPoint:    "Platform Level",
		EndPoint:      "Exit to Jaffa Road",
		WaypointCount: 2,
	},
	{
		Address:       "Rabin Medical Center, Petah Tikva",
		LocationName:  "Beilinson Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Cardiology Wing",
		WaypointCount: 3,
	},
	{
		Address:       "Assuta Medical Center, Ashdod",
		LocationName:  "Assuta Ashdod",
		StartPoint:    "Parking Entrance",
		EndPoint:      "Day Surgery, Floor 2",
		WaypointCount: 2,
	},
	{
		Address:       "Grand Canyon Mall, Haifa",
		LocationName:  "Grand Canyon Haifa",
		StartPoint:    "North Entrance",
		EndPoint:      "Cinema City, Floor 3",
		WaypointCount: 2,
	},
	{
		Address:       "Technion, Haifa",
		LocationName:  "Technion",
		StartPoint:    "Main Gate",
		EndPoint:      "Computer Science Building",
		WaypointCount: 3,
	},
	{
		Address:       "Tel Aviv University, Ramat Aviv",
		LocationName:  "Tel Aviv University",
		StartPoint:    "Gate 2 (Haim Levanon)",
		EndPoint:      "Engineering Faculty",
		WaypointCount: 2,
	},
	{
		Address:       "Malha Mall, Jerusalem",
		LocationName:  "Jerusalem Mall",
		StartPoint:    "Main Entrance",
		EndPoint:      "Bowling Alley, Floor -1",
		WaypointCount: 2,
	},
	{
		Address:       "IKEA Netanya",
		LocationName:  "IKEA Netanya",
		StartPoint:    "Parking Lot",
		EndPoint:      "Restaurant & Cafe",
		WaypointCount: 2,
	},
	{
		Address:       "Soroka Medical Center, Beer Sheva",
		LocationName:  "Soroka Hospital",
		StartPoint:    "Main Entrance",
		EndPoint:      "Pediatric Ward",
		WaypointCount: 3,
	},
	{
		Address:       "Carmel Medical Center, Haifa",
		LocationName:  "Carmel Hospital",
		StartPoint:    "Main Gate",
		EndPoint:      "Maternity Ward",
		WaypointCount: 2,
	},
	{
		Address:       "Sarona Market, Tel Aviv",
		LocationName:  "Sarona Market",
		StartPoint:    "North Entrance",
		EndPoint:      "Indoor Food Hall",
		WaypointCount: 2,
	},
}

// seedImages is the ordered list of test images to cycle through.
var seedImages = []string{
	"pexels-hikaique-114797.jpg",
	"pexels-punttim-240223.jpg",
	"pexels-thecoachspace-2977547.jpg",
	"pexels-janetrangdoan-1024248.jpg",
	"pexels-kyle-miller-169884138-12173424.jpg",
	"pexels-tima-miroshnichenko-5711247.jpg",
	"pexels-divinetechygirl-1181435.jpg",
	"pexels-marta-klement-636760-1438072.jpg",
	"pexels-pixabay-264502.jpg",
	"pexels-tuurt-2954405.jpg",
	"pexels-magda-ehlers-pexels-2861656.jpg",
	"pexels-tuurt-2954412.jpg",
	"pexels-bluemix-12062129.jpg",
	"pexels-pixabay-264512.jpg",
	"pexels-wendywei-4027948.jpg",
	"pexels-poppy-momoa-479654009-18957953.jpg",
	"pexels-shox-29406941.jpg",
	"pexels-sashmere-3861588.jpg",
	"pexels-mavluda-tashbaeva-133603941-10513308.jpg",
	"pexels-bi-ravencrow-2154273033-33327471.jpg",
	"pexels-falak-sabbirbro-photography-1295108-3997553.jpg",
	"pexels-zakh-33659660.jpg",
	"pexels-arthurbrognoli-2260838.jpg",
	"pexels-shkrabaanthony-5264957.jpg",
	"pexels-njeromin-33524440.jpg",
	"pexels-spencer-battista-3582307-5370725.jpg",
	"pexels-the-brainthings-454787989-15617058.jpg",
}

// seedSingleRoute creates, uploads images, waits for processing, and
// publishes one route. imageIndex is the starting offset into seedImages;
// it returns the updated offset so the next call continues the cycle.
func seedSingleRoute(
	t *testing.T,
	route seedRoute,
	authToken string,
	valkeyClient valkeygo.Client,
	imageIndex int,
	routeNum int,
) int {
	t.Helper()

	routeID := prepareRoute(t, authToken)

	waypoints, waypointImages, nextIdx := buildSeedWaypoints(
		t, route, imageIndex,
	)

	createResp := createSeedRoute(
		t, route, routeID, waypoints, authToken, routeNum,
	)

	uploadSeedImages(
		t, createResp, waypointImages, routeNum,
	)

	waitSeedImages(t, createResp, valkeyClient, routeNum)
	publishSeedRoute(t, routeID, authToken, routeNum)

	t.Logf(
		"[%d/%d] published route %s (%s → %s)",
		routeNum,
		len(seedRoutes),
		routeID,
		route.StartPoint,
		route.EndPoint,
	)

	return nextIdx
}

// buildSeedWaypoints builds waypoint request bodies and tracks which
// image filename maps to each position. Returns the next imageIndex.
func buildSeedWaypoints(
	t *testing.T,
	route seedRoute,
	imageIndex int,
) ([]map[string]any, []string, int) {
	t.Helper()

	waypoints := make([]map[string]any, route.WaypointCount)
	waypointImages := make([]string, route.WaypointCount)

	for pos := range route.WaypointCount {
		filename := seedImages[imageIndex%len(seedImages)]
		imageIndex++
		waypointImages[pos] = filename

		imgBytes := loadTestImage(t, filename)
		waypoints[pos] = buildWaypointBody(
			pos, filename, len(imgBytes),
		)
	}

	return waypoints, waypointImages, imageIndex
}

// createSeedRoute calls create-waypoints with custom metadata and
// returns the decoded response.
func createSeedRoute(
	t *testing.T,
	route seedRoute,
	routeID string,
	waypoints []map[string]any,
	authToken string,
	routeNum int,
) CreateWaypointsResponse {
	t.Helper()

	description := route.Description
	if description == "" {
		description = "Navigate from " +
			route.StartPoint +
			" to " +
			route.EndPoint +
			" at " +
			route.LocationName
	}

	body := map[string]any{
		"route_id":       routeID,
		"address":        route.Address,
		"start_point":    route.StartPoint,
		"end_point":      route.EndPoint,
		"location_name":  route.LocationName,
		"description":    description,
		"visibility":     "public",
		"access_method":  "open",
		"lifecycle_type": "permanent",
		"owner_type":     "anonymous",
		"waypoints":      waypoints,
	}

	createURL := apiURL +
		"/api/v1/routes/" +
		routeID +
		"/create-waypoints"

	resp := doRequest(
		t, http.MethodPost, createURL, body, authToken,
	)
	require.Equalf(
		t, http.StatusOK, resp.StatusCode,
		"route %d create-waypoints: expected 200", routeNum,
	)

	var createResp CreateWaypointsResponse

	err := json.NewDecoder(resp.Body).Decode(&createResp)
	resp.Body.Close()
	require.NoErrorf(
		t, err,
		"route %d: decode create-waypoints response", routeNum,
	)

	return createResp
}

// uploadSeedImages uploads all images for a route in parallel.
func uploadSeedImages(
	t *testing.T,
	createResp CreateWaypointsResponse,
	waypointImages []string,
	routeNum int,
) {
	t.Helper()

	t.Logf(
		"[%d/%d] uploading %d images",
		routeNum,
		len(seedRoutes),
		len(createResp.PresignedURLs),
	)

	// Pre-load on main goroutine (loadTestImage uses require).
	preloaded := make(
		map[int][]byte, len(createResp.PresignedURLs),
	)
	for _, entry := range createResp.PresignedURLs {
		preloaded[entry.Position] = loadTestImage(
			t, waypointImages[entry.Position],
		)
	}

	var wg sync.WaitGroup

	uploadErrors := make(
		chan error, len(createResp.PresignedURLs),
	)

	for _, entry := range createResp.PresignedURLs {
		wg.Add(1)

		go func(e PresignedURLEntry, imgBytes []byte) {
			defer wg.Done()

			req, err := http.NewRequest(
				http.MethodPut,
				e.UploadURL,
				bytes.NewReader(imgBytes),
			)
			if err != nil {
				uploadErrors <- fmt.Errorf(
					"image %s: request: %w", e.ImageID, err,
				)
				return
			}

			req.Header.Set(
				"Authorization", "Bearer "+e.UploadToken,
			)

			client := &http.Client{Timeout: 30 * time.Second}

			resp, err := client.Do(req)
			if err != nil {
				uploadErrors <- fmt.Errorf(
					"image %s: upload: %w", e.ImageID, err,
				)
				return
			}
			resp.Body.Close()
		}(entry, preloaded[entry.Position])
	}

	wg.Wait()
	close(uploadErrors)

	for err := range uploadErrors {
		t.Fatalf("route %d upload failed: %v", routeNum, err)
	}
}

// waitSeedImages waits for all images to reach the done stage.
func waitSeedImages(
	t *testing.T,
	createResp CreateWaypointsResponse,
	valkeyClient valkeygo.Client,
	routeNum int,
) {
	t.Helper()

	t.Logf(
		"[%d/%d] waiting for %d images to process",
		routeNum,
		len(seedRoutes),
		len(createResp.PresignedURLs),
	)

	for _, entry := range createResp.PresignedURLs {
		waitForImageStatus(
			t,
			valkeyClient,
			entry.ImageID,
			valkey.StageDone,
			60*time.Second,
		)
	}
}

// publishSeedRoute publishes a route and asserts success.
func publishSeedRoute(
	t *testing.T,
	routeID string,
	authToken string,
	routeNum int,
) {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/publish",
		map[string]any{},
		authToken,
	)
	resp.Body.Close()
	require.Equalf(
		t, http.StatusOK, resp.StatusCode,
		"route %d publish: expected 200, got %d",
		routeNum, resp.StatusCode,
	)
}

// TestSeedSearchData generates 60 published public routes across
// 6 anonymous users for manual testing of the Flutter search/discovery
// UI. Skipped unless SEED_SEARCH_DATA=true is set.
func TestSeedSearchData(t *testing.T) {
	if os.Getenv("SEED_SEARCH_DATA") != "true" {
		t.Skip(
			"SEED_SEARCH_DATA not set — skipping seed data generation",
		)
	}

	const numUsers = 6

	type seedUser struct {
		id    string
		token string
	}

	users := make([]seedUser, numUsers)
	for i := range users {
		id, token := createAnonymousUser(t)
		users[i] = seedUser{id: id, token: token}
		t.Logf("created user %d: %s", i+1, id)
	}

	valkeyClient := newValkeyClient(t)

	imageIndex := 0

	for i, route := range seedRoutes {
		u := users[i%numUsers]
		routeNum := i + 1

		t.Logf(
			"[%d/%d] creating: %s → %s at %s",
			routeNum,
			len(seedRoutes),
			route.StartPoint,
			route.EndPoint,
			route.LocationName,
		)

		imageIndex = seedSingleRoute(
			t, route, u.token, valkeyClient,
			imageIndex, routeNum,
		)
	}

	t.Logf(
		"seed complete: %d routes published across %d users",
		len(seedRoutes),
		numUsers,
	)
}
