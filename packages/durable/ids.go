package durable

// ID is one erased nominal numeric identifier for a durable record kind.
//
// Upstream Pi models IDs as branded numbers (`Id<Kind, Type>`); Go has no
// nominal primitive aliasing, so the brand is erased to int64 and documented.
// IDFromNumber applies the erasure at trusted allocation or decoding
// boundaries. The upstream idFromNumber/seqFromNumber helpers are erased
// trusted casts and are intentionally not a new validation policy for
// already-trusted records.
type ID int64

// Record-kind ID aliases. Go aliases keep the erased int64 representation while
// naming the durable record kind, mirroring upstream branded aliases.
type (
	// ConversationID identifies a conversation.
	ConversationID = ID
	// EntryID identifies a transcript entry.
	EntryID = ID
	// TaskID identifies a durable task.
	TaskID = ID
	// SubmissionID identifies an admitted submission.
	SubmissionID = ID
	// DocumentID identifies one create-to-retire document incarnation.
	DocumentID = ID
)

// Seq is a strictly increasing commit sequence; gaps are permitted.
type Seq int64

// RootConversationID is the reserved ID of the root conversation.
const RootConversationID ConversationID = 1

// MaxSafeInteger is the largest exactly representable integer ID or commit
// sequence value: 2^53-1, the JavaScript safe-integer bound the durable format
// persists.
const MaxSafeInteger int64 = 9007199254740991

// IDFromNumber applies the erased ID brand at a trusted allocation or decoding
// boundary.
func IDFromNumber(value int64) ID { return ID(value) }

// SeqFromNumber applies the erased commit-sequence brand at a trusted storage
// boundary.
func SeqFromNumber(value int64) Seq { return Seq(value) }
