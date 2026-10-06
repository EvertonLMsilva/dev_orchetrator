package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func candidatePolicy() CandidatePolicy {
	return CandidatePolicy{InputTargets: []string{"a"}, WriteTargets: []string{"a", "dir/b"}, Limits: CandidateLimits{MaxOperations: 8, MaxFileBytes: 1024, MaxTotalBytes: 4096, MaxPathBytes: 256, MaxOutputBytes: 8192}}
}
func candidateJSON(edits ...StructuredEdit) []byte {
	b, _ := json.Marshal(StructuredProposal{SchemaVersion: 1, Edits: edits})
	return b
}
func candidateEdit(op WriteFileOperation, target, pre, content string) StructuredEdit {
	return StructuredEdit{Operation: op, Target: target, ExpectedPreimageIdentity: pre, PostimageContent: base64.StdEncoding.EncodeToString([]byte(content)), PostimageIdentity: CandidateDigest([]byte(content))}
}

func TestCandidateStructuredContract(t *testing.T) {
	p := candidatePolicy()
	old := CandidateDigest([]byte("old"))
	before := map[string]CandidateFile{"a": {Target: "a", Identity: old, Mode: 0644, Content: []byte("old")}}
	valid := candidateJSON(candidateEdit(WriteReplace, "a", old, "new"), candidateEdit(WriteCreate, "dir/b", "", "created"))
	if _, err := ValidateStructuredProposal(valid, p, before); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{}
	for name, edit := range map[string]StructuredEdit{"operation": candidateEdit("DELETE", "a", old, "x"), "preimage": candidateEdit(WriteReplace, "a", CandidateDigest(nil), "x"), "createExists": candidateEdit(WriteCreate, "a", "", "x"), "replaceMissing": candidateEdit(WriteReplace, "dir/b", old, "x"), "unauthorized": candidateEdit(WriteCreate, "other", "", "x"), "traversal": candidateEdit(WriteCreate, "../x", "", "x"), "absolute": candidateEdit(WriteCreate, "/x", "", "x"), "git": candidateEdit(WriteCreate, ".git/config", "", "x")} {
		cases[name] = candidateJSON(edit)
	}
	e := candidateEdit(WriteCreate, "dir/b", "", "x")
	e.PostimageIdentity = old
	cases["posthash"] = candidateJSON(e)
	e = candidateEdit(WriteCreate, "dir/b", "", "x")
	e.PostimageContent = "!!"
	cases["encoding"] = candidateJSON(e)
	e = candidateEdit(WriteCreate, "dir/b", "", "x")
	cases["duplicate"] = candidateJSON(e, e)
	cases["conflict"] = candidateJSON(e, candidateEdit(WriteReplace, "dir/b", old, "y"))
	cases["unknown"] = []byte(strings.Replace(string(valid), `"SchemaVersion":1`, `"extra":true,"SchemaVersion":1`, 1))
	cases["duplicateKey"] = []byte(strings.Replace(string(valid), `"SchemaVersion":1`, `"SchemaVersion":1,"SchemaVersion":1`, 1))
	cases["missing"] = []byte(`{"SchemaVersion":1,"Edits":[{"Operation":"CREATE"}]}`)
	cases["null"] = []byte(`{"SchemaVersion":1,"Edits":null}`)
	cases["trailing"] = append(append([]byte(nil), valid...), []byte(` {}`)...)
	cases["fileLimit"] = candidateJSON(candidateEdit(WriteCreate, "dir/b", "", strings.Repeat("x", 1025)))
	cases["outputLimit"] = []byte(strings.Repeat(" ", 8193))
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateStructuredProposal(data, p, before); err == nil {
				t.Fatal("accepted invalid proposal")
			}
		})
	}
	for _, mutate := range []func(*CandidatePolicy){func(p *CandidatePolicy) { p.WriteTargets = []string{"a", "A"} }, func(p *CandidatePolicy) { p.WriteTargets = []string{"a", "a/b"} }, func(p *CandidatePolicy) { p.ExcludedTargets = []string{"a"} }, func(p *CandidatePolicy) { p.Limits.MaxFileBytes = 0 }} {
		bad := p
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("accepted invalid policy")
		}
	}
}

func TestCandidateTrustedLimitsAndStrictFields(t *testing.T) {
	p := candidatePolicy()
	old := CandidateDigest([]byte("old"))
	before := map[string]CandidateFile{"a": {Target: "a", Identity: old, Mode: 0644, Content: []byte("old")}}
	data := candidateJSON(candidateEdit(WriteReplace, "a", old, strings.Repeat("x", 600)), candidateEdit(WriteCreate, "dir/b", "", strings.Repeat("y", 600)))
	for _, name := range []string{"operations", "total", "path", "output"} {
		t.Run(name, func(t *testing.T) {
			q := p.Clone()
			switch name {
			case "operations":
				q.Limits.MaxOperations = 1
			case "total":
				q.Limits.MaxTotalBytes = 1024
			case "path":
				q.Limits.MaxPathBytes = 3
			case "output":
				q.Limits.MaxOutputBytes = int64(len(data) - 1)
			}
			if _, err := ValidateStructuredProposal(data, q, before); err == nil {
				t.Fatal("limit bypass")
			}
		})
	}
	small := candidateJSON(candidateEdit(WriteCreate, "dir/b", "", "x"))
	for name, bad := range map[string][]byte{"editUnknown": []byte(strings.Replace(string(small), `"Target":"dir/b"`, `"Target":"dir/b","Mode":511`, 1)), "editMissing": []byte(strings.Replace(string(small), `"ExpectedPreimageIdentity":"",`, "", 1)), "caseAlias": []byte(strings.Replace(string(small), `"Operation"`, `"operation"`, 1)), "duplicateEditKey": []byte(strings.Replace(string(small), `"Operation":"CREATE"`, `"Operation":"CREATE","Operation":"CREATE"`, 1)), "invalidUTF8": append([]byte{0xff}, small...)} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateStructuredProposal(bad, p, before); err == nil {
				t.Fatal("strict schema bypass")
			}
		})
	}
}
