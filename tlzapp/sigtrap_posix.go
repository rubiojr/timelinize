//go:build !windows && !plan9 && !nacl

/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package tlzapp

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/timelinize/timelinize/timeline"
)

// trapSignalsPosix captures POSIX-only signals.
func trapSignalsPosix() {
	go func() {
		sigchan := make(chan os.Signal, 1)
		signal.Notify(sigchan, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)

		// TODO: implement these...
		for sig := range sigchan {
			switch sig {
			case syscall.SIGQUIT:
				timeline.Log.Warn("SIGQUIT: quitting process immediately")
				os.Exit(2) //nolint:mnd

			case syscall.SIGTERM:
				timeline.Log.Warn("SIGTERM: cleaning up resources, then terminating")
				shutdown(1)

			case syscall.SIGUSR1:
				timeline.Log.Warn("SIGUSR1: reload not implemented")

			case syscall.SIGUSR2:
				timeline.Log.Warn("SIGUSR2: upgrade not implemented")

			case syscall.SIGHUP:
				timeline.Log.Warn("SIGHUP: not implemented")
			}
		}
	}()
}
