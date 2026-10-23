// Package scaffold is the template catalog behind `cafe new` and `cafe init`.
//
// The W6 plan calls cafe "the rails command": `cafe new` is `rails new`, and a
// generator is only as good as the catalog behind it. This package is that
// catalog. It holds the templates, the index that names them, the pure render
// that turns a template plus a name into files, and the one write that puts
// them on disk.
//
// Three rules shape it, and all three come from the failures of the
// alternatives:
//
//   - **The templates are embedded, so the binary is the catalog.** A catalog
//     read from a directory beside the binary is a catalog that differs
//     between a developer's checkout, a release tarball and a CI runner — the
//     same argument the vendored schemas make for themselves. One binary, one
//     catalog.
//
//   - **A scaffold is rendered into memory and refused before anything is
//     written.** A run that hits an existing file part-way through leaves a
//     half-projects tree whose next run refuses the half it had already done.
//     `internal/gen`'s writer holds the same ordering and this package does
//     not grow a second answer to the same question.
//
//   - **The manifest is validated by the same code that lints it.** Every
//     render ends in contract.CheckData before a byte is written, so a template
//     that emits a manifest core's schema rejects is a red test rather than a
//     scaffolded project that fails `cafe contract lint` on a developer's
//     machine. It is also the credential rule: the schema is
//     `additionalProperties: false`, so a manifest carrying a token does not
//     validate, and the generator has no path to write one that passes.
//
// What this package is not: it does not patch the platform's own services.
// Templates scaffold a customer's app; the agent's editable surface is the
// customer's repository, never anywaye's service source. The `platform`
// template composes identity + gateway + courier + parlor as dependencies —
// it references them through the manifest, it does not fork them.
package scaffold
