# Drift Detection Results

Comparison of existing Kubernetes README files against actual code exports.

**Date**: 2026-03-31
**READMEs analyzed**: 8

## Summary

| Metric | Count |
|--------|-------|
| READMEs analyzed | 8 |
| Stale symbol references | 62 |
| Undocumented exports | 203 |
| Stale package references | 5 |

## Per-README Reports

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/client-go/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/client-go`
- **README symbols found**: 7
- **Code exports found**: 0
- **Stale references**: 7
- **Undocumented exports**: 0
- **Stale package refs**: 1

#### Stale References (in README, not in code)

- `GoDocReference`
- `GoDocWidget`
- `HEAD`
- `discovery`
- `dynamic`
- `kubernetes`
- `transport`

#### Stale Package References

- `staging/src/k8s`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/apimachinery/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/apimachinery`
- **README symbols found**: 2
- **Code exports found**: 0
- **Stale references**: 2
- **Undocumented exports**: 0
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `apimachinery`
- `pkg`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/apimachinery/pkg/api/validate/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/apimachinery/pkg/api/validate`
- **README symbols found**: 18
- **Code exports found**: 69
- **Stale references**: 18
- **Undocumented exports**: 69
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `ErrorList`
- `KeysMaxLen`
- `NonEmpty`
- `OtherArgs`
- `ValueType`
- `context.Context`
- `ctx`
- `field.ErrorList`
- `field.Path`
- `fldPath`
- `maxLen`
- `oldValue`
- `opCtx`
- `opCtx.Operation`
- `operation.Create`
- `operation.Operation`
- `validate.Concept`
- `value`

#### Undocumented Exports (in code, not in README)

- `DirectEqual`
- `DirectEqualPtr`
- `Discriminated`
- `DiscriminatedRule`
- `DiscriminatedUnion`
- `EachMapKey`
- `EachMapVal`
- `EachSliceVal`
- `Enum`
- `EnumExclusion`
- `ExtendedResourceName`
- `ExtractorFn`
- `FixedResult`
- `ForbiddenMap`
- `ForbiddenPointer`
- `ForbiddenSlice`
- `ForbiddenValue`
- `GetFieldFunc`
- `IfOption`
- `Immutable`
- `LabelKey`
- `LabelValue`
- `LongName`
- `LongNameCaseless`
- `MatchFunc`
- `MatchItemFn`
- `MaxBytes`
- `MaxItems`
- `MaxLength`
- `Maximum`
- `MinItems`
- `MinLength`
- `Minimum`
- `NEQ`
- `NewDiscriminatedUnionMember`
- `NewDiscriminatedUnionMembership`
- `NewUnionMember`
- `NewUnionMembership`
- `NoModify`
- `NoSet`
- `NoUnset`
- `OptionalMap`
- `OptionalPointer`
- `OptionalSlice`
- `OptionalValue`
- `PathSegmentName`
- `RequiredMap`
- `RequiredPointer`
- `RequiredSlice`
- `RequiredValue`
- ... and 19 more

### /home/ds/kubernetes/kubernetes/vendor/k8s.io/klog/v2/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/vendor/k8s.io/klog/v2`
- **README symbols found**: 26
- **Code exports found**: 134
- **Stale references**: 26
- **Undocumented exports**: 134
- **Stale package refs**: 4

#### Stale References (in README, not in code)

- `EXPERIMENTAL`
- `FlagSet`
- `alsologtostderr`
- `coexist_glog`
- `coexist_klog_v1_and_v2`
- `examples`
- `flag.CommandLine`
- `glog`
- `glog.Fatalf`
- `glog.Info`
- `glog.V`
- `hXRVBH90CgAJ`
- `io.Writer`
- `klog.InitFlags`
- `klog.SetOutput`
- `log_dir`
- `log_file`
- `logtostderr`
- `nItems`
- `set_output`
- `true`
- `usage_log_file`
- `usage_set_output`
- `vX.Y`
- `vX.Y.Z`
- `wCWiWf3Juzs`

#### Undocumented Exports (in code, not in README)

- `Background`
- `CalculateMaxSize`
- `CaptureState`
- `ClearLogger`
- `ContextualLogger`
- `CopyStandardLogTo`
- `EnableContextualLogging`
- `Error`
- `ErrorDepth`
- `ErrorS`
- `ErrorSDepth`
- `Errorf`
- `ErrorfDepth`
- `Errorln`
- `ErrorlnDepth`
- `Exit`
- `ExitDepth`
- `ExitFlushTimeout`
- `Exitf`
- `ExitfDepth`
- `Exitln`
- `ExitlnDepth`
- `Fatal`
- `FatalDepth`
- `Fatalf`
- `FatalfDepth`
- `Fatalln`
- `FatallnDepth`
- `Flush`
- `FlushAndExit`
- `FlushLogger`
- `Format`
- `FromContext`
- `Info`
- `InfoDepth`
- `InfoS`
- `InfoSDepth`
- `Infof`
- `InfofDepth`
- `Infoln`
- `InfolnDepth`
- `InitFlags`
- `KMetadata`
- `KObj`
- `KObjSlice`
- `KObjs`
- `KRef`
- `Level`
- `Level.Get`
- `Level.Set`
- ... and 84 more

#### Stale Package References

- `examples/log_file/usage_log_file`
- `examples/set_output/usage_set_output`
- `klog/v1`
- `klog/v2`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/apiserver/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/apiserver`
- **README symbols found**: 3
- **Code exports found**: 0
- **Stale references**: 3
- **Undocumented exports**: 0
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `apiserver`
- `kubectl`
- `pkg`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/cli-runtime/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/cli-runtime`
- **README symbols found**: 2
- **Code exports found**: 0
- **Stale references**: 2
- **Undocumented exports**: 0
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `kubectl`
- `pkg`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/code-generator/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/code-generator`
- **README symbols found**: 3
- **Code exports found**: 0
- **Stale references**: 3
- **Undocumented exports**: 0
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `CustomResourceDefinition`
- `CustomResources`
- `kube_codegen`

### /home/ds/kubernetes/kubernetes/staging/src/k8s.io/component-base/README.md

- **Code directory**: `/home/ds/kubernetes/kubernetes/staging/src/k8s.io/component-base`
- **README symbols found**: 1
- **Code exports found**: 0
- **Stale references**: 1
- **Undocumented exports**: 0
- **Stale package refs**: 0

#### Stale References (in README, not in code)

- `ComponentConfig`

## Methodology

Drift detection compares:
1. **README symbols**: Backtick-quoted identifiers and PascalCase words extracted from Markdown
2. **Code exports**: Exported Go symbols (functions, types, constants, variables) parsed via go/ast
3. **Package paths**: Directory references in the README checked against actual subdirectories

Common English words matching PascalCase (e.g., 'The', 'This', 'Example') are filtered out.
Test files (*_test.go) are excluded from code export analysis.
