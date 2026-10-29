# stock: the ask tools read their parameters with wordsOf, which takes a parameter the tool does not

runner/runnerd/stock/versetools.go has `wordsOf`, the string half of `paramsOf` (versedata.go) with one difference: paramsOf refuses a parameter the schema does not name, wordsOf takes any key. So `verse-ask-create -questoin …` is refused as an ask with no question, while the same typo on a data tool names the parameter.

The ask tools hand their Params schema to paramsOf and read `.words`; wordsOf goes.