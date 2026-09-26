# Review

Check the change against the issue first: does it do what was asked, and nothing it wasn't asked to do?

Then read beyond the diff. Look at callers of changed functions, other places that share the changed type or config, and error paths the tests skip.

Report a finding only after you confirm it in the code. Point at the file and line, say what input or state breaks, and what happens. Drop anything you can't back with a concrete case.

Count a missing test as a finding only when the untested path could plausibly break. Flag added code that duplicates an existing helper or builds for a need the issue doesn't have.

Keep each summary to one sentence. Put the reasoning in the detail.
