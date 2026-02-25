# Flutter Upload Error UX Improvement Plan

**Status**: Completed
**Priority**: High (User-Facing Quality)
**Affected Repository**: follow-app (`/home/yoseforb/pkg/follow/follow-app/`)
**Estimated Story Points**: 21

---

## Overview

### Problem Statement

When the image-gateway service (port 8090) is down or unreachable, users of
the Follow app see raw exception text in the error dialog and error banner
during route upload. This includes SocketException class names, errno codes,
connection-refused messages, JWT token fragments embedded in URLs, and full
Dart stack traces surfaced through `Exception.toString()`.

The root cause is a single catch-all in `RouteCreationViewModel`:

```dart
// Current code -- exposes raw exception text to the UI
} on Exception catch (e) {
  _uploadError = 'Upload failed: $e';  // $e calls toString() on exception
  ...
}
```

Because `RouteException.toString()` appends `originalError` (a raw
`SocketException` or `http.ClientException`), the string that reaches the
UI widget includes everything: "RouteException: Error uploading image to
gateway - Original: SocketException: Connection refused (OS Error:
Connection refused, errno = 111), address = localhost, port = 8090".

### Business Impact

- Breaks user trust: technical noise suggests the app is broken, not the
  service.
- Leaks infrastructure details: port numbers, hostnames, JWT tokens in URLs,
  and internal error codes are visible to end users.
- Users have no actionable guidance: "Connection refused errno 111" gives no
  path forward. "Try again in a few minutes" does.
- The existing `RouteException` classification helpers (`isNetworkError`,
  `isServerError`, `isClientError`) exist but are never consulted before
  setting `_uploadError`.

### Goal

Replace all raw `$e` / `e.toString()` error strings in the upload flow with
classified, localized, user-friendly messages. No technical details must ever
appear in production UI. Full technical detail must still be logged for
debugging.

---

## Architecture

### Where Classification Must Happen

The MVVM pattern places the responsibility for error message selection in the
**ViewModel**. The ViewModel catches exceptions from the Repository layer,
classifies them, and exposes a clean `String?` to the View. The View uses
the string as-is -- it must not contain any classification logic.

The ViewModel currently lacks `BuildContext` (correct per MVVM), which means
localized strings cannot be produced there. Two valid approaches exist:

**Option A (chosen): Error-type enum from ViewModel, string from View**

The ViewModel exposes an `UploadErrorType` enum value alongside (or instead
of) the raw `_uploadError` string. The View converts the enum to a localized
string via `AppLocalizations` and displays it. The ViewModel's `_uploadError`
field is repurposed to hold the *localized* string injected by the View after
the ViewModel signals which type of error occurred, OR the enum is exposed as
a separate getter and the View is responsible for the string.

**Option B: Pre-classified safe string in ViewModel (English-only fallback)**

The ViewModel sets `_uploadError` to a safe, non-technical English string
that serves as a fallback. The View then maps known string keys to localized
messages, or the ViewModel is passed an error message resolver callback.

Given that:
1. The project already has the pattern of ViewModel setting `_errorMessage`
   strings directly (e.g., the offline message at line 476-478 of the
   ViewModel is hardcoded English -- a pre-existing pattern violation).
2. The `AppLocalizations` approach from the View layer is the correct path
   and is explicitly called out in the CLAUDE.md for this repo.
3. Adding enum exposure keeps the ViewModel clean and testable without any
   string dependency.

**This plan uses Option A**: expose an `UploadErrorType` enum from the
ViewModel, map it to localized strings in the View. The existing `_uploadError`
field is retained as the display-ready string but populated by calling a
pure-Dart helper function (no BuildContext) that maps the exception to an
enum, and the View resolves the enum to an ARB string.

### Error Classification Logic

The classification lives in a new utility:
`lib/ui/route_creation/upload_error_classifier.dart`

This is a pure-Dart file (no Flutter widgets, no BuildContext) with a single
public function and a sealed class / enum for error types. It is easily unit
tested without widget infrastructure.

Classification priority (evaluated top-to-bottom):

1. **Offline / no internet** -- `ConnectivityViewModel.isOffline` is true at
   the time of failure (already checked before upload; catch path also checks).
2. **Connection refused / service down** -- `SocketException` where message
   contains "Connection refused" or `OSError` errno 111/10061. This is the
   primary case when the gateway is down.
3. **Network timeout** -- `TimeoutException` (from `.timeout()` in the HTTP
   client), or `SocketException` with "timed out" message.
4. **No route to host / DNS failure** -- `SocketException` with "No route to
   host", "Network unreachable", or "Failed host lookup".
5. **Server error (5xx)** -- `RouteException.isServerError` is true.
6. **Client error (4xx)** -- `RouteException.isClientError` is true.
   - 401/403: authentication message.
   - 413: file too large message.
   - Other 4xx: generic client error.
7. **Unknown** -- fallback.

### Error Display Strategy

Two surfaces display upload errors today:

1. **Error banner** (persistent, top of `RouteCreationScreen`) -- shows
   `viewModel.errorMessage ?? viewModel.uploadError ?? l10n.routeCreationUnknownError`.
   Currently displays raw exception text.

2. **AlertDialog** (shown after `uploadRouteToServer` returns `false`) --
   shows `viewModel.uploadError ?? l10n.routeCreationUploadFailedMessage`.
   Currently displays raw exception text.

After this plan:
- Both surfaces consume a localized, classified message string.
- The dialog gains a second line with actionable guidance (e.g., "Try again
  in a few minutes" or "Check your connection").
- The dialog retains its Retry and Cancel buttons.
- The banner shows only the short headline message.

---

## New Localization Strings Required

All strings must be added to both `app_en.arb` and `app_he.arb`.

### English strings (`app_en.arb`)

```
uploadErrorServiceUnavailable
  "The image processing service is temporarily unavailable. Please try again in a few minutes."

uploadErrorTimeout
  "Image upload timed out. Check your connection and try again."

uploadErrorOffline
  "You appear to be offline. Your route has been saved as a draft."

uploadErrorServerFailure
  "Something went wrong on our end. Please try again."

uploadErrorAuthFailed
  "Authentication failed. Please restart the app and try again."

uploadErrorFileTooLarge
  "One or more images are too large to upload. Please try with smaller images."

uploadErrorClientRequest
  "Upload failed due to an invalid request. Please try again or contact support."

uploadErrorUnknown
  "Upload failed. Please try again."

uploadErrorPartialSuccess
  "{successCount} of {totalCount} images uploaded successfully. Route published with available images."

uploadErrorAllImagesFailed
  "All images failed to process. Please try uploading again."

uploadErrorDialogGuidanceServiceUnavailable
  "The image service may be starting up. Wait a moment, then tap Retry."

uploadErrorDialogGuidanceTimeout
  "Your network may be slow. Move to a better connection and tap Retry."

uploadErrorDialogGuidanceOffline
  "Connect to the internet, then tap Retry or come back later — your draft is safe."

uploadErrorDialogGuidanceServerFailure
  "This is a temporary server problem. Tap Retry in a moment."

uploadErrorDialogGuidanceGeneric
  "If the problem persists, your draft is saved and you can try again later."
```

### Hebrew strings (`app_he.arb`)

```
uploadErrorServiceUnavailable
  "שירות עיבוד התמונות אינו זמין כעת. נסה שוב בעוד מספר דקות."

uploadErrorTimeout
  "פג הזמן של העלאת התמונה. בדוק את החיבור ונסה שוב."

uploadErrorOffline
  "נראה שאין לך חיבור לאינטרנט. המסלול נשמר כטיוטה."

uploadErrorServerFailure
  "אירעה שגיאה בשרת. נסה שוב."

uploadErrorAuthFailed
  "אימות נכשל. אנא הפעל מחדש את האפליקציה ונסה שוב."

uploadErrorFileTooLarge
  "תמונה אחת או יותר גדולות מדי להעלאה. נסה עם תמונות קטנות יותר."

uploadErrorClientRequest
  "ההעלאה נכשלה עקב בקשה לא תקינה. נסה שוב או פנה לתמיכה."

uploadErrorUnknown
  "ההעלאה נכשלה. נסה שוב."

uploadErrorPartialSuccess
  "{successCount} מתוך {totalCount} תמונות הועלו בהצלחה. המסלול פורסם עם התמונות הזמינות."

uploadErrorAllImagesFailed
  "כל התמונות נכשלו בעיבוד. נסה להעלות שוב."

uploadErrorDialogGuidanceServiceUnavailable
  "ייתכן שהשירות מופעל מחדש. המתן רגע ולחץ על נסה שוב."

uploadErrorDialogGuidanceTimeout
  "הרשת שלך עשויה להיות איטית. עבור לחיבור טוב יותר ולחץ על נסה שוב."

uploadErrorDialogGuidanceOffline
  "התחבר לאינטרנט ולחץ על נסה שוב, או חזור מאוחר יותר — הטיוטה שמורה."

uploadErrorDialogGuidanceServerFailure
  "זוהי תקלה זמנית בשרת. לחץ על נסה שוב בעוד רגע."

uploadErrorDialogGuidanceGeneric
  "אם הבעיה נמשכת, הטיוטה שמורה ותוכל לנסות שוב מאוחר יותר."
```

---

## Files Affected

| File | Change Type | Description |
|------|-------------|-------------|
| `lib/ui/route_creation/upload_error_classifier.dart` | **New** | Pure-Dart error classification utility |
| `lib/ui/route_creation/route_creation_view_model.dart` | **Modify** | Use classifier; expose `UploadErrorType` |
| `lib/ui/route_creation/route_creation_screen.dart` | **Modify** | Map enum to localized strings; improve dialog |
| `lib/l10n/app_en.arb` | **Modify** | Add 15 new error/guidance strings |
| `lib/l10n/app_he.arb` | **Modify** | Add Hebrew translations for same 15 strings |
| `test/ui/route_creation/upload_error_classifier_test.dart` | **New** | Unit tests for classifier |
| `test/ui/route_creation/route_creation_view_model_error_test.dart` | **New** | ViewModel error classification tests |
| `test/ui/route_creation/route_creation_screen_error_test.dart` | **New** | Widget tests for error display |

---

## Tasks

---

### Task 1: Create `UploadErrorType` enum and `UploadErrorClassifier`

**Story Points**: 3

**Description**

Create a new pure-Dart file at
`lib/ui/route_creation/upload_error_classifier.dart` containing:

1. A sealed class or enum `UploadErrorType` with values:
   - `serviceUnavailable` -- connection refused, gateway down
   - `timeout` -- `TimeoutException` or timed-out `SocketException`
   - `offline` -- no route to host, network unreachable, DNS failure
   - `serverFailure` -- 5xx HTTP
   - `authFailed` -- 401 or 403
   - `fileTooLarge` -- 413
   - `clientError` -- other 4xx
   - `unknown` -- all other cases

2. A top-level function:
   ```dart
   UploadErrorType classifyUploadError(Object error)
   ```
   that inspects the exception type and fields in priority order (see
   Architecture section above) and returns the appropriate enum value.

**Key classification rules**:

- If `error` is a `RouteException`:
  - Check `originalError` first for `SocketException`/`TimeoutException`.
  - Then check `statusCode` for HTTP classification.
- If `error` is a raw `Exception` (from the inner catch in
  `_uploadImagesToGateway` / `_uploadImagesWithProgress`):
  - Parse the wrapped exception's message string for "Connection refused",
    "errno = 111", "timed out", "No route to host".
- A `SocketException` with `osError?.errorCode == 111` or message containing
  "Connection refused" maps to `serviceUnavailable`.
- A `TimeoutException` (from `dart:async`) maps to `timeout`.
- A `SocketException` with message containing "timed out" maps to `timeout`.
- A `SocketException` with message containing "No route to host",
  "Network unreachable", or "Failed host lookup" maps to `offline`.

**Files affected**:
- `lib/ui/route_creation/upload_error_classifier.dart` (new)

**Dependencies**: None -- this is a standalone pure-Dart utility.

**Acceptance criteria**:
- File exists at the specified path.
- `classifyUploadError` is a pure function with no side effects.
- No imports of `package:flutter` -- pure Dart only.
- The function handles: `SocketException` (connection refused), `SocketException`
  (no route to host), `TimeoutException`, `RouteException` with 5xx, `RouteException`
  with 401, `RouteException` with 403, `RouteException` with 413, `RouteException`
  with other 4xx, and arbitrary unknown exceptions.
- `dart analyze` returns no errors.

---

### Task 2: Add new localization strings to `app_en.arb` and `app_he.arb`

**Story Points**: 2

**Description**

Add all 15 new strings listed in the "New Localization Strings Required"
section to both ARB files.

Follow the existing ARB file conventions exactly:
- Each key has a `@key` descriptor object with `"description"` and optionally
  `"note"` fields.
- Parameterized strings use the `"placeholders"` pattern with `"type"` and
  `"example"`.
- Hebrew translations follow the Hebrew Translation Guidelines at
  `follow-app/ai-docs/infrastructure/hebrew-translation-guidelines.md`.
- RTL: Hebrew strings must use Hebrew punctuation conventions. The guidance
  string for `offline` should be phrased so that parenthetical structure
  works naturally in RTL.

After adding strings, run `flutter gen-l10n` to regenerate
`AppLocalizations`.

**Files affected**:
- `lib/l10n/app_en.arb`
- `lib/l10n/app_he.arb`

**Dependencies**: None -- can be done in parallel with Task 1.

**Acceptance criteria**:
- All 15 strings exist in both ARB files.
- `flutter gen-l10n` runs with no errors.
- `dart analyze` returns no errors after codegen.
- Parameterized strings (`uploadErrorPartialSuccess`) have correct placeholder
  definitions in the `@` descriptor.
- No hardcoded English text in Hebrew file.

---

### Task 3: Expose `UploadErrorType` from `RouteCreationViewModel`

**Story Points**: 3

**Description**

Modify `RouteCreationViewModel` to:

1. Add a private field:
   ```dart
   UploadErrorType? _uploadErrorType;
   ```

2. Add a public getter:
   ```dart
   UploadErrorType? get uploadErrorType => _uploadErrorType;
   ```

3. Replace the raw catch-all in `uploadRouteToServer`:
   ```dart
   // Before:
   } on Exception catch (e) {
     _uploadError = 'Upload failed: $e';
     logError('Route upload failed', e);
     return false;
   }

   // After:
   } on Exception catch (e) {
     _uploadErrorType = classifyUploadError(e);
     // _uploadError intentionally left null -- View maps type to localized string
     logError('Route upload failed', e);  // Full technical detail preserved in logs
     return false;
   }
   ```

4. Replace the partial failure string (currently hardcoded English at lines
   562-564 of `route_creation_view_model.dart`):
   ```dart
   // Before:
   _uploadError =
       'Some images failed processing. Route published with '
       '$_processingCompleted image(s).';

   // After:
   _uploadErrorType = UploadErrorType.partialSuccess;
   // processingCompleted/processingFailed counts already exposed as getters
   ```
   Add a new `UploadErrorType` value `partialSuccess` for this case.

5. Clear `_uploadErrorType` wherever `_uploadError` is currently cleared
   (search for `_uploadError = null` -- there are three locations: lines 278,
   484, and 1203 of `route_creation_view_model.dart`).

6. Also clear `_uploadErrorType` in the `clearError()` method if it exists,
   or in `reset()`.

**Important**: Do not remove the `_uploadError` field. The `uploadError`
getter is still referenced in the UI and in tests. Set `_uploadError = null`
in all error paths and rely on `_uploadErrorType` for the classification.
The View will use `uploadErrorType` to produce the localized string and can
pass it back, or the View can simply use `uploadErrorType` directly.

**Files affected**:
- `lib/ui/route_creation/route_creation_view_model.dart`
- `lib/ui/route_creation/upload_error_classifier.dart` (adds `partialSuccess`
  value)

**Dependencies**: Task 1 must be complete (classifier must exist).

**Acceptance criteria**:
- `uploadErrorType` getter exists and is typed `UploadErrorType?`.
- Setting `_uploadError = 'Upload failed: $e'` is removed from all upload
  catch blocks.
- `_uploadErrorType` is reset to `null` at every location `_uploadError`
  was previously reset to `null`.
- `logError(...)` calls are retained -- technical detail still logged.
- The offline hardcoded English string at line 476-478 is NOT changed by this
  task (it predates this feature; address if desired as a follow-up).
- `dart analyze` returns no errors.
- Existing gateway tests in `route_creation_view_model_gateway_test.dart`
  continue to pass without modification.

---

### Task 4: Update `RouteCreationScreen` to display classified error messages

**Story Points**: 5

**Description**

Modify `route_creation_screen.dart` to:

1. **Add a helper method** `String _localizeUploadError(AppLocalizations l10n)`
   that maps `viewModel.uploadErrorType` to an ARB string:

   ```dart
   String _localizeUploadError(AppLocalizations l10n) {
     return switch (viewModel.uploadErrorType) {
       UploadErrorType.serviceUnavailable =>
           l10n.uploadErrorServiceUnavailable,
       UploadErrorType.timeout => l10n.uploadErrorTimeout,
       UploadErrorType.offline => l10n.uploadErrorOffline,
       UploadErrorType.serverFailure => l10n.uploadErrorServerFailure,
       UploadErrorType.authFailed => l10n.uploadErrorAuthFailed,
       UploadErrorType.fileTooLarge => l10n.uploadErrorFileTooLarge,
       UploadErrorType.clientError => l10n.uploadErrorClientRequest,
       UploadErrorType.partialSuccess => l10n.uploadErrorPartialSuccess(
           viewModel.processingCompleted,
           viewModel.processingCompleted + viewModel.processingFailed,
         ),
       UploadErrorType.unknown || null => l10n.uploadErrorUnknown,
     };
   }
   ```

2. **Add a guidance helper** `String? _uploadErrorGuidance(AppLocalizations l10n)`
   that returns the second-line actionable text for the dialog (null for
   banner, where only the headline is shown):

   ```dart
   String? _uploadErrorGuidance(AppLocalizations l10n) {
     return switch (viewModel.uploadErrorType) {
       UploadErrorType.serviceUnavailable =>
           l10n.uploadErrorDialogGuidanceServiceUnavailable,
       UploadErrorType.timeout => l10n.uploadErrorDialogGuidanceTimeout,
       UploadErrorType.offline => l10n.uploadErrorDialogGuidanceOffline,
       UploadErrorType.serverFailure =>
           l10n.uploadErrorDialogGuidanceServerFailure,
       _ => l10n.uploadErrorDialogGuidanceGeneric,
     };
   }
   ```

3. **Update the error banner** (around line 348 of `route_creation_screen.dart`)
   to use `_localizeUploadError(l10n)` instead of
   `viewModel.errorMessage ?? viewModel.uploadError ?? l10n.routeCreationUnknownError`.
   Keep `viewModel.errorMessage` (for non-upload errors like connectivity).
   When `viewModel.uploadErrorType != null`, use the new helper. Otherwise
   fall back to `viewModel.errorMessage ?? l10n.routeCreationUnknownError`.

4. **Update the AlertDialog** (around line 1134 of `route_creation_screen.dart`)
   to show a richer content widget with:
   - Primary message: `_localizeUploadError(l10n)` (bold or prominent text)
   - Guidance line: `_uploadErrorGuidance(l10n)` (secondary text, smaller)

   Example structure:
   ```dart
   content: Column(
     mainAxisSize: MainAxisSize.min,
     crossAxisAlignment: CrossAxisAlignment.start,
     children: [
       Text(_localizeUploadError(l10n)),
       const SizedBox(height: 8),
       Text(
         _uploadErrorGuidance(l10n),
         style: Theme.of(context).textTheme.bodySmall?.copyWith(
           color: Theme.of(context).colorScheme.onSurfaceVariant,
         ),
       ),
     ],
   ),
   ```

5. **Verify RTL layout**: The `Column` with `crossAxisAlignment.start`
   respects RTL automatically. Confirm no `EdgeInsets.only(left/right)` are
   added -- use `EdgeInsetsDirectional` if spacing is needed.

6. **Remove any `viewModel.uploadError` references** that now display raw
   strings, replacing them with the classified message. Keep the
   `viewModel.uploadError` getter in the ViewModel for backward compatibility
   with tests that may reference it.

**Files affected**:
- `lib/ui/route_creation/route_creation_screen.dart`

**Dependencies**: Tasks 1, 2, and 3 must be complete.

**Acceptance criteria**:
- Error banner never shows raw exception text. It shows `_localizeUploadError`
  output when `uploadErrorType != null`.
- AlertDialog shows two-line content: main message + guidance.
- AlertDialog retains Retry and Cancel buttons with correct behavior.
- Hebrew locale: both lines render correctly in RTL.
- No `Navigator.pop()` is introduced -- existing `Navigator.pop(dialogContext)`
  in the dialog is acceptable since it uses its own `dialogContext` (this is
  the dialog's own close, not main navigation stack pop).
- `dart analyze` returns no errors.
- No hardcoded English strings are introduced.

---

### Task 5: Unit tests for `UploadErrorClassifier`

**Story Points**: 3

**Description**

Create `test/ui/route_creation/upload_error_classifier_test.dart` with
comprehensive unit tests for `classifyUploadError`.

Use the project's classical/Detroit testing style: no mock frameworks, test
behaviour not interactions, table-driven where applicable.

Test cases required:

| Input | Expected output |
|-------|----------------|
| `SocketException('Connection refused', OSError('Connection refused', 111))` | `serviceUnavailable` |
| `SocketException('Connection refused')` (no OSError) | `serviceUnavailable` |
| `RouteException('...', originalError: SocketException('Connection refused'))` | `serviceUnavailable` |
| `TimeoutException('...')` | `timeout` |
| `SocketException('timed out')` | `timeout` |
| `RouteException('...', originalError: TimeoutException('...'))` | `timeout` |
| `SocketException('No route to host')` | `offline` |
| `SocketException('Network is unreachable')` | `offline` |
| `SocketException('Failed host lookup: ...')` | `offline` |
| `RouteException('...', statusCode: 500)` | `serverFailure` |
| `RouteException('...', statusCode: 503)` | `serverFailure` |
| `RouteException('...', statusCode: 401)` | `authFailed` |
| `RouteException('...', statusCode: 403)` | `authFailed` |
| `RouteException('...', statusCode: 413)` | `fileTooLarge` |
| `RouteException('...', statusCode: 400)` | `clientError` |
| `RouteException('...', statusCode: 422)` | `clientError` |
| `Exception('some unknown error')` | `unknown` |
| `FormatException('...')` | `unknown` |

Use `group` to organise by error category. Use `t.parallel()` equivalent
in Dart (tests are parallel by default in `flutter_test`).

**Files affected**:
- `test/ui/route_creation/upload_error_classifier_test.dart` (new)

**Dependencies**: Task 1 must be complete.

**Acceptance criteria**:
- All table cases pass.
- `flutter test test/ui/route_creation/upload_error_classifier_test.dart`
  exits with code 0.
- No mock frameworks used.
- `dart analyze` returns no errors.

---

### Task 6: ViewModel error classification tests

**Story Points**: 3

**Description**

Create `test/ui/route_creation/route_creation_view_model_error_test.dart`
with tests verifying the ViewModel correctly sets `uploadErrorType` for
different failure scenarios.

Extend the existing fake infrastructure from
`test/ui/route_creation/route_creation_view_model_gateway_test.dart`
(copy and adapt `FakeRouteRepository`, `FakeRouteStatusStreamService`, etc.).

Test cases:

1. **Gateway connection refused**: `FakeRouteRepository.uploadImageToGateway`
   throws `RouteException('Error uploading image to gateway', originalError: SocketException('Connection refused', OSError('Connection refused', 111)))`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.serviceUnavailable`.
   Assert: `viewModel.uploadError` is null (no raw exception text).

2. **Gateway timeout**: repository throws `RouteException('...', originalError: TimeoutException('...'))`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.timeout`.

3. **No network**: repository throws `RouteException('...', originalError: SocketException('No route to host'))`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.offline`.

4. **Server error 503**: repository throws `RouteException('...', statusCode: 503)`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.serverFailure`.

5. **Auth error 401**: repository throws `RouteException('...', statusCode: 401)`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.authFailed`.

6. **Partial image failure**: SSE stream returns 1 `ready` event and 1
   `failed` event for a 2-waypoint route.
   Assert: `viewModel.uploadErrorType == UploadErrorType.partialSuccess`.
   Assert: `viewModel.processingCompleted == 1`.
   Assert: `viewModel.processingFailed == 1`.

7. **All images fail**: SSE returns all `failed` events, 0 `ready`.
   Assert: `viewModel.uploadErrorType == UploadErrorType.allImagesFailed`
   (or `unknown` -- whichever the classifier returns for the
   `RouteException('All images failed to process')` thrown by the ViewModel).
   Confirm the ViewModel's total-failure path also uses the classifier.

8. **Successful upload**: all images succeed. Assert `uploadErrorType` is
   null after successful upload.

9. **Error cleared on retry**: after a failed upload sets `uploadErrorType`,
   starting a new upload must clear `_uploadErrorType` to null before the
   attempt. Assert that `uploadErrorType` is null during the upload phase.

**Files affected**:
- `test/ui/route_creation/route_creation_view_model_error_test.dart` (new)

**Dependencies**: Tasks 1 and 3 must be complete.

**Acceptance criteria**:
- All 9 test cases pass.
- `uploadError` getter returns null in all error cases (raw string no longer set).
- `dart analyze` returns no errors.
- No mock frameworks used.

---

### Task 7: Widget tests for error display in `RouteCreationScreen`

**Story Points**: 2

**Description**

Create `test/ui/route_creation/route_creation_screen_error_test.dart` with
widget tests verifying error display:

1. **Gateway-down scenario**: Pump `RouteCreationScreen` with a ViewModel
   that has `uploadErrorType = UploadErrorType.serviceUnavailable`. Verify:
   - Error banner is visible.
   - Banner text matches `uploadErrorServiceUnavailable` string.
   - No raw exception text, no "SocketException", no "errno", no port numbers.

2. **Timeout scenario**: `uploadErrorType = UploadErrorType.timeout`. Verify
   banner shows `uploadErrorTimeout`.

3. **AlertDialog content**: Simulate `_uploadRoute` returning false with
   `serviceUnavailable` type. Verify dialog shows both the main message and
   the guidance text (`uploadErrorDialogGuidanceServiceUnavailable`).

4. **RTL locale**: Pump with Hebrew locale. Verify banner and dialog render
   without overflow and text direction is RTL.

5. **No error state**: `uploadErrorType` is null. Verify error banner is not
   visible.

Use `MockViewModel` pattern (hand-written stub, not a mock framework) that
allows setting `uploadErrorType` directly.

**Files affected**:
- `test/ui/route_creation/route_creation_screen_error_test.dart` (new)

**Dependencies**: Tasks 2, 3, and 4 must be complete.

**Acceptance criteria**:
- All 5 test cases pass.
- `flutter test test/ui/route_creation/route_creation_screen_error_test.dart`
  exits with code 0.
- Raw exception strings do not appear in any widget tree snapshot.
- `dart analyze` returns no errors.

---

### Task 8: Quality gates and manual QA

**Story Points**: 0 (part of definition of done, not a separate sprint item)

**Description**

Run the full quality gate suite after all code changes are complete:

```bash
cd /home/yoseforb/pkg/follow/follow-app

dart format .
dart analyze                    # Must return "No errors"
dart fix --apply
flutter test --coverage         # >80% coverage required

flutter build apk --debug       # Android primary
flutter build web --debug       # Web mobile
```

Manual QA scenarios to validate:

**Scenario 1: Gateway is down (primary bug)**
1. Stop the image-gateway container (port 8090 unreachable).
2. Create a route with 2 waypoints.
3. Tap "Upload to Server".
4. Observe: error dialog appears.
5. Verify: dialog shows "The image processing service is temporarily
   unavailable. Please try again in a few minutes."
6. Verify: dialog second line shows guidance text.
7. Verify: no SocketException, no "errno", no port numbers, no JWT tokens in
   dialog or banner.
8. Verify: tapping "Cancel" dismisses dialog.
9. Verify: tapping "Retry" re-attempts upload.
10. Start the gateway. Tap "Retry". Verify: upload succeeds.

**Scenario 2: Device offline**
1. Enable airplane mode.
2. Attempt route upload.
3. Verify: banner shows "You appear to be offline. Your route has been saved
   as a draft." (or the offline draft message from the existing offline path).
4. Disable airplane mode. Verify connectivity restores.

**Scenario 3: Slow network / timeout**
1. Throttle network to simulate very slow connection.
2. Attempt route upload. Wait for timeout (30s default).
3. Verify: dialog shows "Image upload timed out. Check your connection and
   try again."

**Scenario 4: Hebrew locale**
1. Switch device to Hebrew.
2. Repeat Scenario 1.
3. Verify: all error strings are in Hebrew, not English fallbacks.
4. Verify: dialog text is right-to-left and does not overflow.

**Scenario 5: Partial processing failure (when gateway is wired)**
1. Arrange for some images to fail gateway processing (requires gateway
   running with simulated failure).
2. Verify: success dialog mentions partial count (e.g., "2 of 3 images
   uploaded successfully.").

**Files affected**: None (manual QA only).

**Dependencies**: All previous tasks must be complete.

**Acceptance criteria**:
- `dart analyze` returns "No errors".
- `flutter test --coverage` passes with coverage >= 80%.
- Both builds succeed without error.
- Manual scenarios 1-4 produce only user-friendly messages (no technical
  strings visible to user).

---

## Implementation Sequence

```
Task 1 (classifier utility)
  |
  +-- Task 2 (ARB strings)   [can run in parallel with Task 1]
  |
Task 3 (ViewModel exposes enum)
  |
  +-- Task 5 (classifier unit tests)   [can run in parallel with Task 4]
  |
Task 4 (Screen display)
  |
  +-- Task 6 (ViewModel error tests)
  |
  +-- Task 7 (Widget tests)
  |
Task 8 (Quality gates + QA)
```

Tasks 1 and 2 have no dependencies and can begin immediately in parallel.
Task 3 depends on Task 1. Tasks 5 and 4 depend on Task 3. Tasks 6 and 7
depend on Tasks 3 and 4. Task 8 is the final gate.

---

## Risk Assessment

### Risk 1: Existing tests break because `uploadError` getter is now always null

**Probability**: Medium. Tests in `route_creation_view_model_gateway_test.dart`
may assert `viewModel.uploadError != null` after a failure.

**Mitigation**: Audit all test files that reference `uploadError` before
starting Task 3. If tests assert the raw string content, update them to
assert `uploadErrorType` instead.

**Files to audit**:
- `test/ui/route_creation/route_creation_view_model_gateway_test.dart`

---

### Risk 2: `dart:async TimeoutException` is not caught by `on Exception`

**Probability**: Low but real. In Dart, `TimeoutException` extends `Exception`
so `on Exception catch (e)` will catch it. However, the inner re-throw pattern
`throw Exception('Failed to upload image ${i + 1}: $e')` wraps the
`TimeoutException` in a plain `Exception`. The classifier must handle both
the wrapped form (inspect the message string) and the direct form.

**Mitigation**: The classifier should inspect the exception's `toString()`
for "TimeoutException" substring when the exception is a plain `Exception`
wrapping a timeout. Cover this in Task 5's test cases.

---

### Risk 3: `SocketException` message text is platform/OS-specific

**Probability**: Low for Android but real. iOS and Android may produce
different `OSError` messages and errno codes for the same network condition.

**Mitigation**: Classify on `osError?.errorCode` (errno) first, fall back to
string matching. errno 111 is `ECONNREFUSED` on Linux/Android. On iOS the
equivalent is errno 61. Cover both in the classifier:

```dart
// ECONNREFUSED: Linux/Android = 111, macOS/iOS = 61
const Set<int> _connectionRefusedErrnos = {61, 111};
```

Include iOS errno 61 in the classifier and in unit tests for Task 5.

---

### Risk 4: Dialog uses `Navigator.pop(dialogContext)` which may conflict with go_router

**Probability**: Very low. The existing dialog code at line 1139 already uses
`Navigator.pop(dialogContext)` with the dialog's own `BuildContext`. This
pattern is documented as acceptable in the project CLAUDE.md because it is
the dialog's own stack entry, not the app navigation stack. No change needed.

---

### Risk 5: `uploadErrorPartialSuccess` parameterized string causes ARB generation error

**Probability**: Low. ARB parameterized strings require correctly typed
placeholder definitions. A missing `"type"` field will cause `flutter gen-l10n`
to fail silently or error.

**Mitigation**: Task 2 must include fully-specified placeholder objects for
`uploadErrorPartialSuccess`:
```json
"@uploadErrorPartialSuccess": {
  "description": "...",
  "placeholders": {
    "successCount": { "type": "int", "example": "2" },
    "totalCount": { "type": "int", "example": "3" }
  }
}
```

---

## Definition of Done

- [ ] `upload_error_classifier.dart` exists with `UploadErrorType` enum and
  `classifyUploadError` function.
- [ ] All 15 new strings are present in `app_en.arb` and `app_he.arb` with
  proper `@` descriptors.
- [ ] `flutter gen-l10n` completes without error.
- [ ] `RouteCreationViewModel` exposes `uploadErrorType` getter.
- [ ] No `'Upload failed: $e'` or similar raw `$e` strings remain in the
  upload flow.
- [ ] `logError(...)` calls are retained throughout -- technical details still
  logged.
- [ ] Error banner in `RouteCreationScreen` uses classified message.
- [ ] AlertDialog shows primary message + guidance text for all error types.
- [ ] `dart analyze` returns "No errors".
- [ ] `flutter test --coverage` passes, coverage >= 80%.
- [ ] `flutter build apk --debug` succeeds.
- [ ] Manual QA Scenario 1 (gateway down) passes: no technical strings visible.
- [ ] Manual QA Scenario 4 (Hebrew) passes: dialog is right-to-left with
  Hebrew strings.
- [ ] All new test files exist and pass.
