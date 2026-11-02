package tools

// maxOutputChars is the most text one document may return. It used to be
// 8000, sized for a chat window; a paid parsing job must not lose the end of
// a long statement or contract, so the limit is now far above any real file.
var maxOutputChars = 20000000
