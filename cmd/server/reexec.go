/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.6.0
 */

package main

import "syscall"

func reexec(path string, argv, environment []string) error {
	return syscall.Exec(path, argv, environment)
}
