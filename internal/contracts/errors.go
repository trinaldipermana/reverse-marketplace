package contracts

// Code classifies failures without parsing text or exposing private policy.
type Code string

const (
	NotFound              Code = "NOT_FOUND"
	InvalidArgument       Code = "INVALID_ARGUMENT"
	InvalidState          Code = "INVALID_STATE"
	PriceBelowMinimum     Code = "PRICE_BELOW_MINIMUM"
	OutOfStock            Code = "OUT_OF_STOCK"
	OfferSuperseded       Code = "OFFER_SUPERSEDED"
	CheckoutExpired       Code = "CHECKOUT_EXPIRED"
	VersionConflict       Code = "VERSION_CONFLICT"
	IdempotencyConflict   Code = "IDEMPOTENCY_CONFLICT"
	DependencyUnavailable Code = "DEPENDENCY_UNAVAILABLE"
)

// Error has a safe explanation for tools and a separate internal cause.
// Cause is available through errors.Is/As and must not be shown to buyers.
// Retryable describes infrastructure retry, not permission to repeat a model
// decision with a different action key.
type Error struct {
	Code      Code
	Message   string
	Retryable bool
	Cause     error `json:"-"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func (e *Error) Unwrap() error { return e.Cause }
