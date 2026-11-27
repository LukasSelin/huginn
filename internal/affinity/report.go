package affinity

import (
	"time"

	"huginn/internal/lineage"
)

// Class is what a field is to its type, judged from how the methods use it.
type Class string

const (
	Entity       Class = "entity"       // state the methods write: stays with the core entity
	Temporary    Class = "temporary"    // state of one operation: a method object
	Config       Class = "config"       // never written by a method: a policy or setting
	Collaborator Class = "collaborator" // a handle on something else, never written: an adapter's
	Infra        Class = "infra"        // locks, loggers, metrics: every new type gets its own
	Unused       Class = "unused"       // no method uses it
)

// BlockKind is what a block of fields and methods would become.
type BlockKind string

const (
	Core    BlockKind = "core"    // the largest block of state: what remains of the type
	State   BlockKind = "state"   // other state with methods of its own: a type of its own
	Service BlockKind = "service" // configuration or collaborators with the methods using them
	Shared  BlockKind = "shared"  // methods needing only a hub, such as the options
)

// RowKind is how a method was placed.
type RowKind string

const (
	Member RowKind = "member" // in a block
	Bridge RowKind = "bridge" // only calls methods of several blocks: orchestration
	Free   RowKind = "free"   // touches no field and calls no method: a function
)

// Sym names one symbol of the history.
type Sym struct {
	ID   lineage.ID `json:"id"`
	Name string     `json:"name"`
	Path string     `json:"path"`
	Line int        `json:"line"`
}

// Report is the affinity analysis of one type. Columns are its fields and
// rows its methods, both in display order: block by block. Every reference
// to a column, row or block is an index into Columns, Rows or Blocks.
type Report struct {
	Type    Sym    `json:"type"`
	Dir     string `json:"dir"`
	Package string `json:"package"` // import path
	Struct  bool   `json:"struct"`  // a struct type; others have no columns

	Months int       `json:"months"` // the window of history; 0 for all of it
	From   time.Time `json:"from"`
	To     time.Time `json:"to"` // the last commit

	Columns []Column `json:"columns"`
	Rows    []Row    `json:"rows"`
	Blocks  []Block  `json:"blocks"`

	// LCOM4 counts the groups of methods linked by a shared field or a call,
	// before hubs and orchestration are set aside; Split counts the core,
	// state and service blocks after.
	LCOM4 int `json:"lcom4"`
	Split int `json:"split"`

	// Unattributed counts the receiver selectors naming neither a field nor
	// a method of the type, such as promoted members that could not be
	// traced to an embedded field.
	Unattributed int `json:"unattributed"`

	Findings []Finding `json:"findings"`
	Coupling Coupling  `json:"coupling"`
	Clients  Clients   `json:"clients"`
	Names    Names     `json:"names"`
	Steps    []Step    `json:"steps"`
	Notes    []Note    `json:"notes"`
}

// Column is one field of the type.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Embedded bool   `json:"embedded,omitempty"`
	Decl     int    `json:"decl"`  // position among the fields
	Group    int    `json:"group"` // declaration group, see ast.Member
	Line     int    `json:"line"`

	Class  Class `json:"class"`
	Hub    bool  `json:"hub,omitempty"`    // used by most methods; left out of clustering
	Shared bool  `json:"shared,omitempty"` // state written from two blocks; see the sharedState finding
	Ctor   bool  `json:"ctor,omitempty"`   // set where the type is constructed

	Block   int    `json:"block"` // -1 for hubs, infrastructure and unused fields
	Readers int    `json:"readers"`
	Writers int    `json:"writers"`
	Reason  string `json:"reason"`
}

// Row is one method of the type.
type Row struct {
	Sym
	Method    string `json:"method"`
	Signature string `json:"signature"`
	Exported  bool   `json:"exported"`
	Decl      int    `json:"decl"` // position in source order
	Stmts     int    `json:"stmts"`

	Cells []Cell `json:"cells"`
	Calls []Call `json:"calls"`

	Kind   RowKind  `json:"kind"`
	Block  int      `json:"block"` // -1 for bridges and free methods
	Reason string   `json:"reason"`
	Notes  []string `json:"notes"`

	Edits    int `json:"edits"`    // changes in the window
	Clients  int `json:"clients"`  // clients calling it
	Internal int `json:"internal"` // calls from the type's own methods
}

// Cell is how a method uses a field.
type Cell struct {
	Col   int    `json:"col"`
	Read  bool   `json:"r,omitempty"`
	Write bool   `json:"w,omitempty"`
	Count int    `json:"n"`             // occurrences
	Via   string `json:"via,omitempty"` // the call that writes it, "Store", or "promoted"
}

// Call is one method calling another of the type, or using it as a value.
type Call struct {
	Row   int  `json:"row"`
	Value bool `json:"value,omitempty"`
	Count int  `json:"n"`
}

// Block is a group of fields and the methods placed with them: a candidate
// type, or for a shared block a group of methods needing only a hub.
type Block struct {
	Letter  string    `json:"letter"`
	Kind    BlockKind `json:"kind"`
	Label   string    `json:"label"`
	Columns []int     `json:"columns"`
	Rows    []int     `json:"rows"`
	Reason  string    `json:"reason"`

	// Exclusive reports that only its methods write its fields and they
	// write no other block's.
	Exclusive bool       `json:"exclusive"`
	Crossings []Crossing `json:"crossings"`

	Edits    int      `json:"edits"`
	Commits  int      `json:"commits"`
	Cohesion *float64 `json:"cohesion,omitempty"` // co-change weight inside against inside and across; two methods or more

	Clients   int       `json:"clients"`
	Agreement *float64  `json:"agreement,omitempty"` // share of its clients using no other block
	Word      string    `json:"word,omitempty"`      // the word most of its members share
	Outside   []Outside `json:"outside"`             // symbols elsewhere changing with it
}

// Crossing is a dependency between a block and another: one of its methods
// using a field or calling a method of another block (Out), or another
// block's method using one of its fields.
type Crossing struct {
	Row    int  `json:"row"`
	Col    int  `json:"col"`    // -1 for a call
	Callee int  `json:"callee"` // -1 for a field use
	Out    bool `json:"out"`
	Write  bool `json:"write"`
}

// Finding is one pattern in the matrix, with what to do about it.
type Finding struct {
	Kind    string `json:"kind"`
	Rows    []int  `json:"rows"`
	Cols    []int  `json:"cols"`
	Blocks  []int  `json:"blocks"`
	Text    string `json:"text"`
	Advice  string `json:"advice"`
	Callers []Sym  `json:"callers,omitempty"`
}

// Coupling is how the type's methods changed together in the window.
type Coupling struct {
	Commits   int    `json:"commits"`   // commits changing any of its methods, as counted
	MassEdits int    `json:"massEdits"` // commits left out for changing more than Limit methods
	Limit     int    `json:"limit"`
	Min       int    `json:"min"` // co-changes that make a pair strong
	Pairs     []Pair `json:"pairs"`
}

// Pair is two methods that changed in the same commits.
type Pair struct {
	A       int     `json:"a"`
	B       int     `json:"b"`
	N       int     `json:"n"`              // commits changing both
	Logical float64 `json:"logical"`        // see cochange.Logical
	Strong  bool    `json:"strong"`         // at least the minimum co-changes
	Cross   bool    `json:"cross"`          // in different blocks
	Kind    string  `json:"kind,omitempty"` // for a strong cross pair: boundary, duplicated or concept
	Text    string  `json:"text,omitempty"`
}

// Outside is a symbol outside the type that changed with a block.
type Outside struct {
	Sym
	N     int     `json:"n"`
	Share float64 `json:"share"` // of the block's commits
}

// Clients are the code calling the type's methods: packages, or files of
// its own package.
type Clients struct {
	List     []Client   `json:"list"`
	Roles    []Role     `json:"roles"`
	Hubs     []int      `json:"hubs"` // methods most clients call, left out of the roles
	Uncalled []Uncalled `json:"uncalled"`
}

// Client is one package, or one file of the type's own package, calling its
// methods.
type Client struct {
	Name        string      `json:"name"`
	Package     bool        `json:"package"`
	Uses        []ClientUse `json:"uses"`
	Callers     []Sym       `json:"callers"`
	Blocks      []int       `json:"blocks"`
	Role        int         `json:"role"`
	Text        string      `json:"text"`
	Interface   string      `json:"interface,omitempty"` // a consumer-side interface, for a package
	Approximate bool        `json:"approximate,omitempty"`
}

// ClientUse is a client's use of one method.
type ClientUse struct {
	Row         int  `json:"row"`
	Calls       int  `json:"calls"`
	Value       bool `json:"value,omitempty"`
	Approximate bool `json:"approximate,omitempty"`
}

// Role is a group of clients using the same methods.
type Role struct {
	Clients []int  `json:"clients"`
	Rows    []int  `json:"rows"`
	Blocks  []int  `json:"blocks"`
	Text    string `json:"text"`
}

// Uncalled is a method nothing calls.
type Uncalled struct {
	Row int    `json:"row"`
	Why string `json:"why"`
}

// Names is what the members' names say.
type Names struct {
	Words  []Word  `json:"words"`
	Clumps []Clump `json:"clumps"`
	Flags  []Flag  `json:"flags"`
	Groups []Group `json:"groups"`
}

// Word is a word shared by the names of two or more members.
type Word struct {
	Word   string `json:"word"`
	Rows   []int  `json:"rows"`
	Cols   []int  `json:"cols"`
	Blocks []int  `json:"blocks"`
}

// Clump is fields used by exactly the same methods.
type Clump struct {
	Cols []int `json:"cols"`
	Rows []int `json:"rows"`
}

// Flag is a field that looks like a mode or a state.
type Flag struct {
	Col     int    `json:"col"`
	Readers []int  `json:"readers"`
	Why     string `json:"why"`
}

// Group is a run of fields declared together.
type Group struct {
	Group  int   `json:"group"`
	Cols   []int `json:"cols"`
	Blocks []int `json:"blocks"`
	Agrees bool  `json:"agrees"` // within one block
}

// Step is one move of the suggested extraction order.
type Step struct {
	Kind    string   `json:"kind"` // free, extract, narrow, remains
	Block   int      `json:"block"`
	Rows    []int    `json:"rows"`
	Text    string   `json:"text"`
	Signals []string `json:"signals"`
}

// Note is a place where the signals disagree.
type Note struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Blocks []int  `json:"blocks"`
	Rows   []int  `json:"rows"`
}

// Candidate is a type that may do too much.
type Candidate struct {
	Sym
	Dir     string  `json:"dir"`
	Methods int     `json:"methods"`
	Fields  int     `json:"fields"`
	Split   int     `json:"split"`
	LCOM4   int     `json:"lcom4"`
	Edits   int     `json:"edits"`
	Score   float64 `json:"score"`
}
