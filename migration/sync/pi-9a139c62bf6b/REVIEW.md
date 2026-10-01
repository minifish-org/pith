# Review: no embedded-SDK implementation change

This was the first source-file change after the pinned baseline, but it is not the first behavioral change to the Pith embedded SDK.

All changes in Pico3, Chord delta tracking, service publication and replicated state are outside the original accepted Pith scope. The new Chord barrel exports and type interfaces do not change the existing Context, JsonValue or JsonRepresentation behavior represented by Pith. Do not introduce these excluded services merely to exercise the migration tool.

The Pico3 spec-view-events test is reference context in the original ai-surface plan, not ownership of the AI module. Its inferred Go ownership was invalid and is removed from the reviewed sync configuration. Reference inputs are watched separately from implementation mappings.

The draft and original sync report are retained as evidence of conservative discovery, not accepted scope. No implementation, model execution or upstream-baseline advance is authorized by this draft. The old config checksum is superseded. Keep the workflow planned; do not execute it.

The replacement first increment targets 002fc8385268300ca91a5fc95f935c2afbbdac02, the provider stream observer capability through AI, agent core and the embedded coding-agent SDK. Its cumulative predecessor changes must receive explicit scope dispositions.
