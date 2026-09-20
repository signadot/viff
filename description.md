# charter: a spawn naming a capability nobody provides is not caught at install

Install checks a rule's fields, but nothing checks a spawn's provider capability against a doer that could perform it — docs/provider-selection.md leaves it to the firing, so an unprovidable capability is found when it fires rather than when it installs.

From docs/working/work/questions.md, which asked it as 'if a charter spawns a capability nobody provides, you find out at first firing rather than at install'.