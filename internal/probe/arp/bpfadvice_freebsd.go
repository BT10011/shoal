package arp

// bpfAdvice says how to get past a refused BPF device on FreeBSD, where
// nothing opens them to an ordinary account.
const bpfAdvice = "run shoal as root (sudo shoal)"
