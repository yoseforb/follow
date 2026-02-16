# Relative Marker Coordinates Migration Plan

## Overview

Migrate waypoint marker coordinates from absolute pixel integers (0-10000) to relative/normalized float64 (0.0-1.0) before integrating the image gateway. This migration simplifies the entire system by:

1. **Eliminating redundant conversions** - Flutter already uses normalized coordinates internally; converting to pixels and back is wasteful
2. **Making markers scale-invariant** - When the image gateway resizes images, markers remain correct without recalculation
3. **Simplifying gateway integration** - The route domain doesn't need to know about image processing; no cross-domain events needed

### Architecture Impact

- **Database**: Column type change from `INTEGER` to `DOUBLE PRECISION`, rename columns
- **API Contract**: Goa DSL types change from `Int` to `Float64`, update validation ranges
- **Domain Entities**: Field types change from `int` to `float64`, update constants and validation
- **Repository Layer**: SQL query updates, row mapping changes
- **Flutter**: Field types change from `int` to `double`, simplify coordinate handling

### Key Integration Points

- **follow-api**: Backend API server (Go, PostgreSQL, Goa-Design)
- **follow-app**: Flutter mobile app (Dart)

### Technical Approach

**Order of implementation**: follow-api first (API contract is source of truth), then follow-app.

**Migration strategy**: Since this is MVP with no production data, we can change the schema directly without data migration.

### Success Criteria

- All quality gates pass in both repositories
- API contract uses `Float64` with 0.0-1.0 range
- Database uses `DOUBLE PRECISION` with CHECK constraints
- Flutter uses `double` and works directly with normalized coordinates
- No conversion between pixels and normalized in Flutter UI layer
- All tests updated and passing

---

## Implementation Tasks

### Phase 1: Database Schema Migration (follow-api)

#### Task 1.1: Create Database Migration

**Files to create:**
- `migrations/016_route_waypoints_relative_coordinates.sql`

**Description:**
Create a new migration that changes marker coordinate columns from INTEGER to DOUBLE PRECISION and renames them.

**Changes:**
```sql
-- Up migration
ALTER TABLE route.waypoints
  ALTER COLUMN marker_x_coordinate TYPE DOUBLE PRECISION,
  ALTER COLUMN marker_y_coordinate TYPE DOUBLE PRECISION;

ALTER TABLE route.waypoints
  RENAME COLUMN marker_x_coordinate TO marker_x;

ALTER TABLE route.waypoints
  RENAME COLUMN marker_y_coordinate TO marker_y;

-- Drop old constraint
ALTER TABLE route.waypoints
  DROP CONSTRAINT IF EXISTS valid_marker_coordinates;

-- Add new constraint for normalized range
ALTER TABLE route.waypoints
  ADD CONSTRAINT valid_marker_coordinates
    CHECK (
      marker_x >= 0.0 AND marker_x <= 1.0 AND
      marker_y >= 0.0 AND marker_y <= 1.0
    );

-- Down migration
-- Reverse the changes (rename back, change type back, restore old constraint)
```

**Dependencies:** None

**Acceptance Criteria:**
- Migration file follows naming convention (prefix with next number: 016)
- Up migration changes column types and renames columns
- CHECK constraint enforces 0.0-1.0 range
- Down migration restores original schema
- Migration runs successfully: `go run ./cmd/server -runtime-timeout 10s`

**Story Points:** 2

---

### Phase 2: Goa DSL API Contract Updates (follow-api)

#### Task 2.1: Update Constants

**Files to modify:**
- `design/constants.go`

**Changes:**
```go
// Remove MaxMarkerCoordinate constant (no longer needed)
// Or update its documentation to indicate it's deprecated

// Add new constants for normalized coordinates
const (
  MinMarkerCoordinate = 0.0  // Minimum normalized coordinate
  MaxMarkerCoordinate = 1.0  // Maximum normalized coordinate
)
```

**Dependencies:** None

**Acceptance Criteria:**
- Constants reflect new normalized coordinate system
- Documentation updated

**Story Points:** 1

---

#### Task 2.2: Update WaypointInput Type

**Files to modify:**
- `design/route_types.go`

**Changes:**
```go
// WaypointInput - change marker fields from Int to Float64
var WaypointInput = Type("WaypointInput", func() {
    Description("Input data for creating a single waypoint")

    Attribute("marker_x", Float64, func() {
        Description("X coordinate of marker (normalized 0.0-1.0, where 0.0=left, 1.0=right)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.35)
    })
    Attribute("marker_y", Float64, func() {
        Description("Y coordinate of marker (normalized 0.0-1.0, where 0.0=top, 1.0=bottom)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.24)
    })
    // ... other fields ...
    Required(
        "marker_x",
        "marker_y",
        // ... other required fields ...
    )
})
```

**Note:** Rename field from `marker_x_coordinate` to `marker_x`, `marker_y_coordinate` to `marker_y` for brevity.

**Dependencies:** Task 2.1

**Acceptance Criteria:**
- Fields renamed to `marker_x` and `marker_y`
- Type changed from `Int` to `Float64`
- Validation range changed to 0.0-1.0
- Descriptions updated to reflect normalized coordinates

**Story Points:** 2

---

#### Task 2.3: Update WaypointInfo Type

**Files to modify:**
- `design/route_types.go`

**Changes:**
```go
// WaypointInfo - change marker fields from Int to Float64
var WaypointInfo = Type("WaypointInfo", func() {
    Description("Waypoint information for navigation")

    Attribute("marker_x", Float64, func() {
        Description("X coordinate of marker (normalized 0.0-1.0)")
        Example(0.35)
    })
    Attribute("marker_y", Float64, func() {
        Description("Y coordinate of marker (normalized 0.0-1.0)")
        Example(0.24)
    })
    // ... other fields ...
    Required(
        // ... update required fields to use marker_x, marker_y
    )
})
```

**Dependencies:** Task 2.1

**Acceptance Criteria:**
- Fields renamed to `marker_x` and `marker_y`
- Type changed from `Int` to `Float64`
- Descriptions updated

**Story Points:** 1

---

#### Task 2.4: Update UpdateWaypointPayload Type

**Files to modify:**
- `design/route_types.go`

**Changes:**
```go
// UpdateWaypointPayload - change marker fields from Int to Float64
var UpdateWaypointPayload = Type("UpdateWaypointPayload", func() {
    // ... existing fields ...

    Attribute("marker_x", Float64, func() {
        Description("Updated X coordinate of marker (normalized 0.0-1.0, optional)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.35)
    })
    Attribute("marker_y", Float64, func() {
        Description("Updated Y coordinate of marker (normalized 0.0-1.0, optional)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.24)
    })
    // ... other fields ...
})
```

**Dependencies:** Task 2.1

**Acceptance Criteria:**
- Fields renamed to `marker_x` and `marker_y`
- Type changed from `Int` to `Float64`
- Validation range 0.0-1.0

**Story Points:** 1

---

#### Task 2.5: Update ReplaceWaypointImageConfirmPayload Type

**Files to modify:**
- `design/route_types.go`

**Changes:**
```go
// ReplaceWaypointImageConfirmPayload
var ReplaceWaypointImageConfirmPayload = Type("ReplaceWaypointImageConfirmPayload", func() {
    // ... existing fields ...

    Attribute("marker_x", Float64, func() {
        Description("Updated X coordinate of marker (normalized 0.0-1.0)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.35)
    })
    Attribute("marker_y", Float64, func() {
        Description("Updated Y coordinate of marker (normalized 0.0-1.0)")
        Minimum(0.0)
        Maximum(1.0)
        Example(0.24)
    })
    Required(
        "image_id",
        "file_hash",
        "marker_x",
        "marker_y",
    )
})
```

**Dependencies:** Task 2.1

**Acceptance Criteria:**
- Fields renamed to `marker_x` and `marker_y`
- Type changed to `Float64`
- Validation range 0.0-1.0

**Story Points:** 1

---

#### Task 2.6: Update ReplaceWaypointImageConfirmResult Type

**Files to modify:**
- `design/route_types.go`

**Changes:**
```go
// ReplaceWaypointImageConfirmResult
var ReplaceWaypointImageConfirmResult = ResultType(..., func() {
    // ... existing fields ...

    Field(5, "marker_x", Float64, func() {
        Description("Updated X coordinate of marker (normalized 0.0-1.0)")
        Example(0.35)
    })
    Field(6, "marker_y", Float64, func() {
        Description("Updated Y coordinate of marker (normalized 0.0-1.0)")
        Example(0.24)
    })
    Required(
        // ... update required fields
    )
})
```

**Dependencies:** Task 2.1

**Acceptance Criteria:**
- Fields renamed and type changed
- Descriptions updated

**Story Points:** 1

---

#### Task 2.7: Update route_service.go Goa DSL Endpoints

**Files to modify:**
- `design/route_service.go`

**Changes:**
Update any route service endpoint definitions that reference marker coordinates in their Field definitions or examples. Search for `marker_x_coordinate` and `marker_y_coordinate` references and update to `marker_x`, `marker_y` with `Float64` type.

**Dependencies:** Tasks 2.2-2.6

**Acceptance Criteria:**
- All endpoint definitions use `marker_x`, `marker_y` with `Float64`
- No references to old field names remain

**Story Points:** 1

---

#### Task 2.8: Regenerate Goa Code

**Files affected:**
- `gen/` directory (generated code, do not edit manually)

**Commands:**
```bash
cd /home/yoseforb/pkg/follow/follow-api
goa gen follow-api/design
```

**Dependencies:** Tasks 2.1-2.7

**Acceptance Criteria:**
- Goa generation completes without errors
- Generated types reflect new field names and types
- Server compiles: `go build ./cmd/server`

**Story Points:** 1

---

### Phase 3: Domain Entity Updates (follow-api)

#### Task 3.1: Update Waypoint Entity Constants and Fields

**Files to modify:**
- `internal/domains/route/domain/entities/waypoint.go`

**Changes:**
```go
// Update constants
const (
    MaxWaypointDescriptionLength = 1000
    MinMarkerCoordinate          = 0.0
    MaxMarkerCoordinate          = 1.0
)

// Waypoint struct - change marker fields to float64
type Waypoint struct {
    // ... existing fields ...

    // Marker information (denormalized for navigation performance)
    markerX      float64  // Normalized 0.0-1.0
    markerY      float64  // Normalized 0.0-1.0
    markerType   *valueobjects.MarkerType

    // ... existing fields ...
}

// Update NewWaypoint constructor signature
func NewWaypoint(
    routeID uuid.UUID,
    imageID uuid.UUID,
    markerX, markerY float64,  // Changed from int
    markerType *valueobjects.MarkerType,
    description string,
) (*Waypoint, error) {
    // ... update field assignments ...
    waypoint := &Waypoint{
        // ...
        markerX: markerX,
        markerY: markerY,
        // ...
    }
    return waypoint, nil
}

// Update NewWaypointFromStorage
func NewWaypointFromStorage(
    id, routeID, imageID uuid.UUID,
    markerX, markerY float64,  // Changed from int
    markerType *valueobjects.MarkerType,
    status *valueobjects.WaypointStatus,
    description string,
    createdAt, updatedAt time.Time,
) (*Waypoint, error) {
    // ... update field assignments ...
}

// Update getter methods
func (w *Waypoint) MarkerX() float64 {
    return w.markerX
}

func (w *Waypoint) MarkerY() float64 {
    return w.markerY
}

// Update GetMarkerCoordinates
func (w *Waypoint) GetMarkerCoordinates() (float64, float64) {
    return w.markerX, w.markerY
}

// Update UpdateMarkerCoordinates
func (w *Waypoint) UpdateMarkerCoordinates(x, y float64) error {
    err := validateMarkerCoordinates(x, y)
    if err != nil {
        return err
    }
    w.markerX = x
    w.markerY = y
    w.updatedAt = time.Now().UTC()
    return nil
}

// Update validation
func validateMarkerCoordinates(x, y float64) error {
    if x < MinMarkerCoordinate || y < MinMarkerCoordinate {
        return domain.ErrWaypointInvalidMarkerCoords
    }
    if x > MaxMarkerCoordinate || y > MaxMarkerCoordinate {
        return domain.ErrWaypointInvalidMarkerCoords
    }
    return nil
}

// Update String() method
func (w *Waypoint) String() string {
    return fmt.Sprintf(
        "Waypoint{ID: %s, Route: %s, Image: %s, Marker: (%.3f,%.3f)}",
        w.id.String(),
        w.routeID.String(),
        w.imageID.String(),
        w.markerX,
        w.markerY,
    )
}
```

**Dependencies:** Task 2.8 (after Goa regeneration)

**Acceptance Criteria:**
- Field names changed from `markerXCoordinate`, `markerYCoordinate` to `markerX`, `markerY`
- Field types changed from `int` to `float64`
- Constants updated to 0.0 and 1.0
- Validation logic updated for float64 range
- All method signatures updated
- Code compiles

**Story Points:** 3

---

#### Task 3.2: Update Waypoint Entity Tests

**Files to modify:**
- `internal/domains/route/domain/entities/waypoint_test.go`

**Changes:**
- Update all test fixtures to use `float64` values (e.g., `0.35`, `0.24` instead of `350`, `240`)
- Update assertions to check `float64` values
- Update error test cases for new validation range (0.0-1.0)
- Update table-driven tests

**Dependencies:** Task 3.1

**Acceptance Criteria:**
- All entity tests pass: `go test ./internal/domains/route/domain/entities/...`
- Test coverage maintained or improved

**Story Points:** 2

---

### Phase 4: Repository Layer Updates (follow-api)

#### Task 4.1: Update Repository SQL Queries

**Files to modify:**
- `internal/domains/route/repository/postgres/waypoint_repository_impl.go`

**Changes:**
Update all SQL queries to use new column names:
- Change `marker_x_coordinate` to `marker_x`
- Change `marker_y_coordinate` to `marker_y`

Functions to update:
- `Save()` - INSERT statement
- `FindByID()` - SELECT statement
- `FindByRouteID()` - SELECT statement
- `Update()` - UPDATE statement
- `UpdateWithImage()` - UPDATE statement (for image replacement)
- Any other methods with marker coordinate references

Example:
```go
// Old:
// marker_x_coordinate, marker_y_coordinate, marker_type, ...
// New:
// marker_x, marker_y, marker_type, ...
```

**Dependencies:** Task 1.1 (migration must be run first)

**Acceptance Criteria:**
- All SQL queries use new column names
- No references to `_coordinate` suffix remain
- Code compiles

**Story Points:** 2

---

#### Task 4.2: Update waypointRow Struct

**Files to modify:**
- `internal/domains/route/repository/postgres/waypoint_repository_impl.go`

**Changes:**
```go
type waypointRow struct {
    id                string
    routeID           string
    imageID           string
    markerX           float64  // Changed from int
    markerY           float64  // Changed from int
    markerType        string
    status            string
    description       sql.NullString
    createdAt         time.Time
    updatedAt         time.Time
}
```

Update all row scanning code to scan into `float64` fields.

**Dependencies:** Task 4.1

**Acceptance Criteria:**
- Struct fields are `float64`
- Row scanning works correctly
- Code compiles

**Story Points:** 1

---

#### Task 4.3: Update Row Mapping Functions

**Files to modify:**
- `internal/domains/route/repository/postgres/waypoint_repository_impl.go`

**Changes:**
Update `rowToWaypoint()` or similar mapping functions to handle `float64` marker coordinates:

```go
func rowToWaypoint(row waypointRow) (*entities.Waypoint, error) {
    // ... existing code ...

    return entities.NewWaypointFromStorage(
        id,
        routeID,
        imageID,
        row.markerX,  // Now float64
        row.markerY,  // Now float64
        markerType,
        status,
        description,
        row.createdAt,
        row.updatedAt,
    )
}
```

**Dependencies:** Task 4.2

**Acceptance Criteria:**
- Mapping functions handle `float64` correctly
- Code compiles

**Story Points:** 1

---

#### Task 4.4: Update Repository Integration Tests

**Files to modify:**
- `internal/domains/route/repository/postgres/waypoint_repository_integration_test.go`
- `internal/domains/route/repository/postgres/route_repository_integration_test.go`

**Changes:**
- Update all test fixtures to use `float64` marker coordinates
- Update assertions to check `float64` values
- Update table-driven tests

**Dependencies:** Tasks 4.1-4.3

**Acceptance Criteria:**
- All repository integration tests pass
- Test coverage maintained

**Story Points:** 2

---

### Phase 5: Use Case Layer Updates (follow-api)

#### Task 5.1: Update CreateRouteWithWaypointsPayload

**Files to modify:**
- `internal/domains/route/usecases/create_route_with_waypoints_usecase.go`
- `internal/domains/route/interfaces/types.go`

**Changes:**
```go
// In types.go
type WaypointInputData struct {
    MarkerX           float64  `json:"marker_x" validate:"min=0.0,max=1.0"`
    MarkerY           float64  `json:"marker_y" validate:"min=0.0,max=1.0"`
    MarkerType        string   `json:"marker_type" validate:"required,oneof=next_step final_destination"`
    ImageMetadata     ImageMetadataInput `json:"image_metadata" validate:"required"`
    Description       string   `json:"description,omitempty" validate:"max=1000"`
}
```

Update use case to use `float64`:
```go
// In create_route_with_waypoints_usecase.go
waypoint, err := entities.NewWaypoint(
    route.ID(),
    imageID,
    wpInput.MarkerX,  // Now float64
    wpInput.MarkerY,  // Now float64
    markerType,
    wpInput.Description,
)
```

**Dependencies:** Task 3.1 (entity updated)

**Acceptance Criteria:**
- Input types use `float64` with validation range 0.0-1.0
- Use case creates waypoints with normalized coordinates
- Code compiles

**Story Points:** 2

---

#### Task 5.2: Update UpdateWaypointCommand

**Files to modify:**
- `internal/domains/route/module/commands.go`
- `internal/domains/route/usecases/update_waypoint_usecase.go`

**Changes:**
```go
// In commands.go
type UpdateWaypointCommand struct {
    RouteID           uuid.UUID
    WaypointID        uuid.UUID
    Description       *string
    MarkerX           *float64  `json:"marker_x" validate:"omitempty,min=0.0,max=1.0"`
    MarkerY           *float64  `json:"marker_y" validate:"omitempty,min=0.0,max=1.0"`
    MarkerType        *string   `json:"marker_type" validate:"omitempty,oneof=next_step final_destination"`
}
```

Update use case validation and error messages:
```go
// In update_waypoint_usecase.go
if cmd.MarkerX != nil {
    if *cmd.MarkerX < 0.0 || *cmd.MarkerX > 1.0 {
        return nil, fmt.Errorf(
            "%w: marker_x must be between 0.0 and 1.0",
            ErrInvalidInput,
        )
    }
}
// Similar for MarkerY
```

**Dependencies:** Task 3.1

**Acceptance Criteria:**
- Command uses `float64` pointers for optional updates
- Validation enforces 0.0-1.0 range
- Error messages updated to reflect normalized coordinates

**Story Points:** 2

---

#### Task 5.3: Update ReplaceWaypointImageConfirm Types

**Files to modify:**
- `internal/domains/route/usecases/confirm_replace_waypoint_image_usecase.go`

**Changes:**
Update the confirm payload and use case to use `float64`:

```go
type ConfirmReplaceWaypointImageInput struct {
    // ... existing fields ...
    MarkerX  float64  `json:"marker_x" validate:"min=0.0,max=1.0"`
    MarkerY  float64  `json:"marker_y" validate:"min=0.0,max=1.0"`
}

// Update use case:
err = waypoint.UpdateMarkerCoordinates(input.MarkerX, input.MarkerY)
```

**Dependencies:** Task 3.1

**Acceptance Criteria:**
- Input uses `float64` with validation
- Use case updates marker coordinates correctly
- Error messages updated

**Story Points:** 1

---

#### Task 5.4: Update Use Case Tests

**Files to modify:**
- `internal/domains/route/usecases/create_route_with_waypoints_usecase_test.go`
- `internal/domains/route/usecases/update_waypoint_usecase_test.go`
- `internal/domains/route/usecases/confirm_replace_waypoint_image_usecase_test.go`
- `internal/domains/route/usecases/mocks_test.go`

**Changes:**
- Update all test fixtures to use `float64` marker coordinates (e.g., `0.1`, `0.2` instead of `100`, `200`)
- Update mock waypoint creation to use `float64`
- Update assertions
- Update error validation tests for 0.0-1.0 range

**Dependencies:** Tasks 5.1-5.3

**Acceptance Criteria:**
- All use case tests pass
- Test coverage maintained
- Mocks use normalized coordinates

**Story Points:** 3

---

### Phase 6: API Service Layer Updates (follow-api)

#### Task 6.1: Update Goa Service Implementations

**Files to modify:**
- `internal/api/services/routes_service.go`

**Changes:**
Update all Goa service methods that map between Goa types and domain types:

1. `CreateRouteWithWaypoints` - map marker coordinates from Goa types to use case input
2. `UpdateWaypoint` - map optional marker coordinate updates
3. `PrepareReplaceWaypointImage` - if it handles markers
4. `ConfirmReplaceWaypointImage` - map marker coordinates from confirm payload

Update validation and error messages:
```go
// Old validation:
if wp.MarkerXCoordinate < 0 || wp.MarkerXCoordinate > 10000 {
    return nil, fmt.Errorf("%w: marker_x_coordinate must be between 0 and 10000", ...)
}

// New validation:
if wp.MarkerX < 0.0 || wp.MarkerX > 1.0 {
    return nil, fmt.Errorf("%w: marker_x must be between 0.0 and 1.0", ...)
}
```

Update result building to use new field names.

**Dependencies:** Task 2.8 (Goa regeneration)

**Acceptance Criteria:**
- All service methods use `marker_x`, `marker_y` with `float64`
- Validation enforces 0.0-1.0 range
- Error messages updated
- Code compiles

**Story Points:** 3

---

#### Task 6.2: Update API Service Tests

**Files to modify:**
- Any test files for `routes_service.go`

**Changes:**
- Update test fixtures to use `float64` marker coordinates
- Update assertions
- Update validation tests

**Dependencies:** Task 6.1

**Acceptance Criteria:**
- All API service tests pass
- Test coverage maintained

**Story Points:** 2

---

### Phase 7: Integration Testing and Quality Gates (follow-api)

#### Task 7.1: Run All Quality Gates

**Commands:**
```bash
cd /home/yoseforb/pkg/follow/follow-api

# Format code
gofumpt -w .
golines -w --max-len=80 .

# Lint
go vet ./...
./custom-gcl run -c .golangci-custom.yml ./...

# Test
go test -race -cover ./...

# Clean dependencies
go mod tidy

# Verify server starts
go run ./cmd/server -runtime-timeout 10s
```

**Dependencies:** All previous follow-api tasks

**Acceptance Criteria:**
- All quality gates pass
- No linting errors
- All tests pass with race detection
- Server starts without errors

**Story Points:** 2

---

#### Task 7.2: Run Integration Test Script

**Commands:**
```bash
cd /home/yoseforb/pkg/follow/follow-api
./scripts/test-route-workflow.sh -t 15s --port 8083
```

**Changes if needed:**
Update `scripts/test-route-workflow.sh` if it has hardcoded marker coordinate values that need to change from pixel integers to normalized floats.

**Dependencies:** Task 7.1

**Acceptance Criteria:**
- Integration test script passes
- Route creation workflow works end-to-end with normalized coordinates
- Image uploads and confirmations succeed

**Story Points:** 2

---

### Phase 8: Flutter Domain Model Updates (follow-app)

#### Task 8.1: Update Waypoint Model

**Files to modify:**
- `lib/domain/models/waypoint.dart`

**Changes:**
```dart
class Waypoint {
  const Waypoint({
    required this.id,
    required this.routeId,
    required this.position,
    required this.markerX,          // Renamed from markerXCoordinate
    required this.markerY,          // Renamed from markerYCoordinate
    required this.markerType,
    required this.imageId,
    this.description,
    this.navigationInstructions,
  });

  // ... existing fields ...

  /// X coordinate of marker (normalized 0.0-1.0, where 0.0=left, 1.0=right)
  final double markerX;

  /// Y coordinate of marker (normalized 0.0-1.0, where 0.0=top, 1.0=bottom)
  final double markerY;

  /// Creates a placeholder Waypoint
  factory Waypoint.placeholder({
    required String routeId,
    required int position,
  }) {
    return Waypoint(
      id: 'placeholder-$routeId-$position',
      routeId: routeId,
      position: position,
      markerX: 0.0,   // Changed from int to double
      markerY: 0.0,   // Changed from int to double
      markerType: WaypointMarkerType.nextStep,
      imageId: 'placeholder-image-$routeId-$position',
    );
  }

  /// Creates a Waypoint from JSON
  factory Waypoint.fromJson(Map<String, dynamic> json) {
    try {
      // Parse marker_x and marker_y as double
      double? markerX;
      try {
        // Handle both int and double from API
        final dynamic markerXValue = json['marker_x'];
        if (markerXValue is int) {
          markerX = markerXValue.toDouble();
        } else {
          markerX = markerXValue as double;
        }
      } catch (e) {
        throw FormatException(
          'marker_x is null or not a number: ${json['marker_x']} ($e)',
        );
      }

      double? markerY;
      try {
        final dynamic markerYValue = json['marker_y'];
        if (markerYValue is int) {
          markerY = markerYValue.toDouble();
        } else {
          markerY = markerYValue as double;
        }
      } catch (e) {
        throw FormatException(
          'marker_y is null or not a number: ${json['marker_y']} ($e)',
        );
      }

      // Validate range
      if (markerX < 0.0 || markerX > 1.0) {
        throw FormatException('marker_x must be between 0.0 and 1.0: $markerX');
      }
      if (markerY < 0.0 || markerY > 1.0) {
        throw FormatException('marker_y must be between 0.0 and 1.0: $markerY');
      }

      return Waypoint(
        // ... existing fields ...
        markerX: markerX,
        markerY: markerY,
        // ... existing fields ...
      );
    } catch (e) {
      throw FormatException('Invalid Waypoint JSON structure: $e');
    }
  }

  /// Converts to JSON
  Map<String, dynamic> toJson() {
    return <String, dynamic>{
      'waypoint_id': id,
      'route_id': routeId,
      'position': position,
      'marker_x': markerX,
      'marker_y': markerY,
      'marker_type': markerType.value,
      'image_id': imageId,
      if (description != null) 'description': description,
      if (navigationInstructions != null)
        'navigation_instructions': navigationInstructions,
    };
  }

  /// copyWith
  Waypoint copyWith({
    String? id,
    String? routeId,
    int? position,
    double? markerX,
    double? markerY,
    WaypointMarkerType? markerType,
    String? imageId,
    String? description,
    String? navigationInstructions,
  }) {
    return Waypoint(
      id: id ?? this.id,
      routeId: routeId ?? this.routeId,
      position: position ?? this.position,
      markerX: markerX ?? this.markerX,
      markerY: markerY ?? this.markerY,
      markerType: markerType ?? this.markerType,
      imageId: imageId ?? this.imageId,
      description: description ?? this.description,
      navigationInstructions: navigationInstructions ?? this.navigationInstructions,
    );
  }

  @override
  bool operator ==(Object other) {
    // ... update to compare markerX, markerY as double ...
  }

  @override
  int get hashCode {
    // ... update to hash markerX, markerY ...
  }
}

// Update WaypointInput
class WaypointInput {
  const WaypointInput({
    required this.markerX,   // Changed from markerXCoordinate
    required this.markerY,   // Changed from markerYCoordinate
    required this.markerType,
    required this.imageMetadata,
    this.description,
    this.navigationInstructions,
  });

  final double markerX;
  final double markerY;
  final WaypointMarkerType markerType;
  final ImageMetadata imageMetadata;
  final String? description;
  final String? navigationInstructions;

  Map<String, dynamic> toJson() {
    return <String, dynamic>{
      'marker_x': markerX,
      'marker_y': markerY,
      'marker_type': markerType.value,
      'image_metadata': imageMetadata.toJson(),
      if (description != null) 'description': description,
      if (navigationInstructions != null)
        'navigation_instructions': navigationInstructions,
    };
  }
}
```

**Dependencies:** Task 7.2 (follow-api complete)

**Acceptance Criteria:**
- Field names changed to `markerX`, `markerY`
- Types changed to `double`
- JSON serialization uses `marker_x`, `marker_y` (snake_case for API)
- Validation added for 0.0-1.0 range in fromJson
- All documentation updated

**Story Points:** 3

---

### Phase 9: Flutter UI Layer Updates (follow-app)

#### Task 9.1: Update Marker Placement Screen

**Files to modify:**
- `lib/ui/route_creation/marker_placement_screen.dart`

**Changes:**
The marker placement screen already works with normalized coordinates internally (`_markerX`, `_markerY` as `double?` with 0.0-1.0 range). The key change is to **remove the conversion to pixels** in `_confirmMarker()`:

```dart
/// Confirms the marker placement and returns the result.
Future<void> _confirmMarker() async {
  if (_markerX == null || _markerY == null) {
    return;
  }

  // SIMPLIFIED: Just return normalized coordinates directly
  // No need to load image dimensions or convert to pixels
  if (!mounted) return;

  Navigator.of(context).pop(<String, dynamic>{
    'markerX': _markerX,   // Normalized 0.0-1.0
    'markerY': _markerY,   // Normalized 0.0-1.0
    // Remove 'markerXPixels' and 'markerYPixels'
  });
}
```

**Dependencies:** Task 8.1

**Acceptance Criteria:**
- Screen returns normalized coordinates directly (no pixel conversion)
- Marker placement still works correctly with BoxFit.contain
- No unnecessary image dimension loading for pixel conversion

**Story Points:** 2

---

#### Task 9.2: Update Route Creation Screen

**Files to modify:**
- `lib/ui/route_creation/route_creation_screen.dart`

**Changes:**
Update the code that receives marker placement results:

```dart
// Old code (around line 958):
markerXCoordinate: (configData!['markerXPixels'] as int).toDouble(),
markerYCoordinate: (configData['markerYPixels'] as int).toDouble(),

// New code:
markerX: configData!['markerX'] as double,
markerY: configData['markerY'] as double,
```

Remove any references to `markerXPixels` or `markerYPixels`.

**Dependencies:** Task 9.1

**Acceptance Criteria:**
- Uses normalized coordinates from marker placement screen
- No references to pixel coordinates

**Story Points:** 1

---

#### Task 9.3: Update Route Creation ViewModel

**Files to modify:**
- `lib/ui/route_creation/route_creation_view_model.dart`

**Changes:**
Update all references to use normalized coordinates directly:

```dart
// Old code (around lines 558-559, 770-771):
markerXCoordinate: waypointData.markerXCoordinate.round(),
markerYCoordinate: waypointData.markerYCoordinate.round(),

// New code:
markerX: waypointData.markerX,
markerY: waypointData.markerY,

// Old code (around lines 923-924):
markerXCoordinate: waypoint.markerXCoordinate.toDouble(),
markerYCoordinate: waypoint.markerYCoordinate.toDouble(),

// New code:
markerX: waypoint.markerX,
markerY: waypoint.markerY,
```

Remove `.round()` and `.toDouble()` conversions since we're already working with `double`.

**Dependencies:** Task 9.2

**Acceptance Criteria:**
- ViewModel uses normalized coordinates directly
- No unnecessary type conversions

**Story Points:** 2

---

#### Task 9.4: Update Waypoint Manager

**Files to modify:**
- `lib/ui/route_creation/waypoint_manager.dart`

**Changes:**
Update `WaypointData` class and references:

```dart
class WaypointData {
  const WaypointData({
    required this.markerX,   // Renamed from markerXCoordinate
    required this.markerY,   // Renamed from markerYCoordinate
    // ... other fields ...
  });

  final double markerX;
  final double markerY;
  // ... other fields ...

  // Update toWaypointInput method (around line 123):
  WaypointInput toWaypointInput() {
    return WaypointInput(
      markerX: markerX,       // No .round() needed
      markerY: markerY,       // No .round() needed
      markerType: markerType,
      imageMetadata: imageMetadata,
      description: description,
    );
  }

  // Update copyWith
  WaypointData copyWith({
    double? markerX,
    double? markerY,
    // ... other fields ...
  }) {
    return WaypointData(
      markerX: markerX ?? this.markerX,
      markerY: markerY ?? this.markerY,
      // ... other fields ...
    );
  }

  // Update toString
  @override
  String toString() {
    return 'WaypointData(marker: ($markerX, $markerY), '
           // ... rest ...
  }
}
```

**Dependencies:** Task 9.3

**Acceptance Criteria:**
- WaypointData uses normalized coordinates
- No type conversions needed

**Story Points:** 1

---

#### Task 9.5: Simplify MarkerPositionCalculator

**Files to modify:**
- `lib/utils/marker_position_calculator.dart`

**Changes:**
The `MarkerPositionCalculator` already has methods for normalized coordinates. We can **remove** or **deprecate** the pixel conversion methods since they're no longer needed:

```dart
/// DEPRECATED: No longer needed - API now uses normalized coordinates
///
/// Converts normalized coordinates (0.0-1.0) to pixel coordinates
/// on the original image.
@Deprecated('API now uses normalized coordinates directly')
static Map<String, int> normalizedToPixelCoordinates({
  // ... keep implementation for backward compatibility if needed ...
}) {
  // ... existing implementation ...
}

/// DEPRECATED: No longer needed - API now uses normalized coordinates
///
/// Converts pixel coordinates to normalized coordinates (0.0-1.0).
@Deprecated('API now uses normalized coordinates directly')
static Map<String, double> pixelToNormalizedCoordinates({
  // ... keep implementation for backward compatibility if needed ...
}) {
  // ... existing implementation ...
}
```

Or simply remove these methods entirely if not used elsewhere.

**Dependencies:** Tasks 9.1-9.4

**Acceptance Criteria:**
- Deprecated or removed pixel conversion methods
- Core functionality (calculateImageBounds, calculateMarkerPosition) unchanged

**Story Points:** 1

---

#### Task 9.6: Update Marker Overlay Widget

**Files to modify:**
- `lib/ui/navigation/widgets/marker_overlay.dart`

**Changes:**
Update `WaypointMarkerOverlay` to accept normalized coordinates directly:

```dart
class WaypointMarkerOverlay extends StatelessWidget {
  const WaypointMarkerOverlay({
    required this.markerX,    // Changed from pixel int to normalized double
    required this.markerY,    // Changed from pixel int to normalized double
    required this.markerType,
    required this.containerWidth,
    required this.containerHeight,
    required this.originalImageWidth,
    required this.originalImageHeight,
    this.markerSize = 48.0,
    this.animated = true,
    this.zoomScale = 1.0,
    super.key,
  });

  /// X coordinate of marker (normalized 0.0-1.0)
  final double markerX;

  /// Y coordinate of marker (normalized 0.0-1.0)
  final double markerY;

  // ... rest of fields ...

  @override
  Widget build(BuildContext context) {
    // SIMPLIFIED: markerX and markerY are already normalized, use directly
    // No need to convert from pixels

    // Calculate actual rendered image bounds
    final ImageBounds bounds = MarkerPositionCalculator.calculateImageBounds(
      containerWidth: containerWidth,
      containerHeight: containerHeight,
      imageWidth: originalImageWidth,
      imageHeight: originalImageHeight,
    );

    // Calculate marker position using normalized coordinates directly
    final Offset markerPosition =
        MarkerPositionCalculator.calculateMarkerPosition(
          normalizedX: markerX,    // Already normalized
          normalizedY: markerY,    // Already normalized
          bounds: bounds,
          markerSize: markerSize,
        );

    // ... rest of build ...
  }
}
```

Update documentation:
```dart
/// Example usage:
/// ```dart
/// WaypointMarkerOverlay(
///   markerX: 0.35,   // Normalized coordinates
///   markerY: 0.24,
///   // ... other params ...
/// )
/// ```
```

**Dependencies:** Task 8.1

**Acceptance Criteria:**
- Widget accepts normalized coordinates (`double`)
- No pixel-to-normalized conversion needed
- Documentation updated

**Story Points:** 2

---

#### Task 9.7: Update Waypoint Viewer

**Files to modify:**
- `lib/ui/navigation/waypoint_viewer.dart`

**Changes:**
Update the code that passes marker coordinates to the overlay:

```dart
// Old code (around lines 395-396):
markerX: widget.waypoint.markerXCoordinate,
markerY: widget.waypoint.markerYCoordinate,

// New code:
markerX: widget.waypoint.markerX,
markerY: widget.waypoint.markerY,
```

**Dependencies:** Task 9.6

**Acceptance Criteria:**
- Uses new field names
- Navigation display works correctly

**Story Points:** 1

---

### Phase 10: Flutter Repository and Data Layer Updates (follow-app)

#### Task 10.1: Update Route Repository

**Files to modify:**
- `lib/data/repositories/route_repository.dart`

**Changes:**
Update debug logging around lines 1006-1009:

```dart
// Old:
'Waypoint $i marker_x_coordinate: ${wpJson['marker_x_coordinate']}',
'Waypoint $i marker_y_coordinate: ${wpJson['marker_y_coordinate']}',

// New:
'Waypoint $i marker_x: ${wpJson['marker_x']} (normalized)',
'Waypoint $i marker_y: ${wpJson['marker_y']} (normalized)',
```

Verify that API request building uses the correct field names (`marker_x`, `marker_y`).

**Dependencies:** Task 8.1

**Acceptance Criteria:**
- Logging uses new field names
- API requests use correct JSON structure

**Story Points:** 1

---

### Phase 11: Flutter Testing (follow-app)

#### Task 11.1: Update Domain Model Tests

**Files to modify:**
- `test/domain/models/route_test.dart`

**Changes:**
Update all test fixtures to use normalized coordinates:

```dart
// Old:
markerXCoordinate: 100,
markerYCoordinate: 200,

// New:
markerX: 0.1,
markerY: 0.2,

// Update JSON tests:
'marker_x_coordinate': 100,  // Old
'marker_x': 0.1,             // New
```

**Dependencies:** Task 8.1

**Acceptance Criteria:**
- All model tests pass
- Fixtures use normalized coordinates

**Story Points:** 2

---

#### Task 11.2: Update Service Tests

**Files to modify:**
- `test/data/services/route_storage_service_test.dart`

**Changes:**
Update test fixtures:

```dart
// Old:
markerXCoordinate: 100,
markerYCoordinate: 200,

markerXCoordinate: 300,
markerYCoordinate: 400,

// New:
markerX: 0.1,
markerY: 0.2,

markerX: 0.3,
markerY: 0.4,
```

**Dependencies:** Task 8.1

**Acceptance Criteria:**
- All service tests pass

**Story Points:** 1

---

#### Task 11.3: Run All Flutter Quality Gates

**Commands:**
```bash
cd /home/yoseforb/pkg/follow/follow-app

# Format
dart format .

# Analyze
dart analyze

# Fix
dart fix --apply

# Test
flutter test --coverage
```

**Dependencies:** All previous follow-app tasks

**Acceptance Criteria:**
- `dart analyze` returns "No errors"
- All tests pass
- Test coverage >80%

**Story Points:** 2

---

### Phase 12: Documentation and Final Validation

#### Task 12.1: Update API Documentation

**Files to modify (follow-api):**
- `docs/entities/waypoint.md` (if exists)
- Any ADR or architecture docs that reference marker coordinates

**Changes:**
- Update documentation to reflect normalized coordinates
- Update examples to use 0.0-1.0 values
- Update field names from `marker_x_coordinate` to `marker_x`

**Dependencies:** Task 7.1

**Acceptance Criteria:**
- All documentation updated
- Examples use normalized coordinates

**Story Points:** 1

---

#### Task 12.2: Update Flutter Documentation

**Files to modify (follow-app):**
- Update any README or architecture docs that reference marker coordinates

**Changes:**
- Document that markers use normalized coordinates
- Update examples

**Dependencies:** Task 11.3

**Acceptance Criteria:**
- Documentation reflects normalized coordinate system

**Story Points:** 1

---

#### Task 12.3: End-to-End Validation

**Manual testing:**
1. Start follow-api server
2. Run follow-app
3. Create a new route with waypoints
4. Place markers on images
5. Verify markers are stored and displayed correctly
6. Test image replacement flow
7. Test waypoint update flow
8. Verify navigation display

**Dependencies:** Tasks 7.2, 11.3

**Acceptance Criteria:**
- Route creation works end-to-end
- Markers display correctly in navigation
- Image replacement preserves marker positions
- No pixel/normalized conversion issues

**Story Points:** 3

---

## Task Summary

### Total Story Points by Phase

1. **Phase 1** (Database): 2 points
2. **Phase 2** (Goa DSL): 9 points
3. **Phase 3** (Domain Entities): 5 points
4. **Phase 4** (Repository): 6 points
5. **Phase 5** (Use Cases): 8 points
6. **Phase 6** (API Services): 5 points
7. **Phase 7** (Integration Testing): 4 points
8. **Phase 8** (Flutter Models): 3 points
9. **Phase 9** (Flutter UI): 10 points
10. **Phase 10** (Flutter Data): 1 point
11. **Phase 11** (Flutter Testing): 5 points
12. **Phase 12** (Documentation): 5 points

**Total: 63 story points**

### Implementation Order

**Critical path:**
1. Database migration (Task 1.1)
2. Goa DSL updates and regeneration (Tasks 2.1-2.8)
3. Domain entities (Tasks 3.1-3.2)
4. Repository layer (Tasks 4.1-4.4)
5. Use case layer (Tasks 5.1-5.4)
6. API services (Tasks 6.1-6.2)
7. Quality gates (Tasks 7.1-7.2)
8. Flutter domain models (Task 8.1)
9. Flutter UI (Tasks 9.1-9.7)
10. Flutter data layer (Task 10.1)
11. Flutter testing (Tasks 11.1-11.3)
12. Documentation and validation (Tasks 12.1-12.3)

**follow-api must be complete before starting follow-app** (API contract is source of truth).

---

## Risk Assessment

### Potential Challenges

1. **Database migration** - Must handle migration carefully, but since this is MVP with no production data, we can change schema directly
2. **Type conversions** - Ensure all int/float conversions are handled correctly in both Go and Dart
3. **Test coverage** - Many test files need updates; must maintain coverage
4. **Goa regeneration** - Regenerated code may have formatting issues that need to be fixed

### Mitigation Strategies

1. **Quality gates after each phase** - Catch issues early
2. **Run migration in test environment first**
3. **Update tests incrementally** - Don't let test failures accumulate
4. **Manual E2E testing** - Verify the complete flow works

---

## Resource Requirements

### Specialized Agents Recommended

1. **backend-database-engineer**: Tasks 1.1, 4.1-4.4 (database and repository)
2. **backend-domain-specialist**: Tasks 3.1-3.2, 5.1-5.4 (domain and use cases)
3. **backend-api-specialist**: Tasks 2.1-2.8, 6.1-6.2 (Goa DSL and services)
4. **frontend-flutter-specialist**: Tasks 8.1, 9.1-9.7, 10.1 (Flutter domain and UI)
5. **test-automation-specialist**: Tasks 3.2, 4.4, 5.4, 6.2, 11.1-11.3 (all tests)
6. **code-quality-reviewer**: Tasks 7.1-7.2, 11.3, 12.3 (quality gates and validation)
7. **documentation-specialist**: Tasks 12.1-12.2 (documentation updates)

### External Dependencies

- PostgreSQL running locally
- MinIO running locally
- Flutter SDK installed
- Go 1.23+ installed

---

## Appendix: Key Files Changed

### follow-api (Go)

**Database:**
- `migrations/016_route_waypoints_relative_coordinates.sql` (new)

**Goa DSL:**
- `design/constants.go`
- `design/route_types.go`
- `design/route_service.go`
- `gen/` (regenerated)

**Domain:**
- `internal/domains/route/domain/entities/waypoint.go`
- `internal/domains/route/domain/entities/waypoint_test.go`

**Repository:**
- `internal/domains/route/repository/postgres/waypoint_repository_impl.go`
- `internal/domains/route/repository/postgres/waypoint_repository_integration_test.go`
- `internal/domains/route/repository/postgres/route_repository_integration_test.go`

**Use Cases:**
- `internal/domains/route/interfaces/types.go`
- `internal/domains/route/module/commands.go`
- `internal/domains/route/usecases/create_route_with_waypoints_usecase.go`
- `internal/domains/route/usecases/update_waypoint_usecase.go`
- `internal/domains/route/usecases/confirm_replace_waypoint_image_usecase.go`
- `internal/domains/route/usecases/*_test.go`
- `internal/domains/route/usecases/mocks_test.go`

**API Services:**
- `internal/api/services/routes_service.go`

**Scripts:**
- `scripts/test-route-workflow.sh` (may need updates)

### follow-app (Flutter)

**Domain:**
- `lib/domain/models/waypoint.dart`

**UI:**
- `lib/ui/route_creation/marker_placement_screen.dart`
- `lib/ui/route_creation/route_creation_screen.dart`
- `lib/ui/route_creation/route_creation_view_model.dart`
- `lib/ui/route_creation/waypoint_manager.dart`
- `lib/ui/navigation/waypoint_viewer.dart`
- `lib/ui/navigation/widgets/marker_overlay.dart`

**Utils:**
- `lib/utils/marker_position_calculator.dart`

**Data:**
- `lib/data/repositories/route_repository.dart`

**Tests:**
- `test/domain/models/route_test.dart`
- `test/data/services/route_storage_service_test.dart`

---

## Next Steps

After completing this plan:

1. **Proceed with Valkey integration** - The route domain is now ready for image gateway integration without needing to know about image resizing
2. **Consider additional optimizations** - Normalized coordinates open doors for more efficient marker positioning algorithms
3. **Monitor for edge cases** - Watch for any floating-point precision issues in production
