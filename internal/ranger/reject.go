// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package ranger

// RejectMessage is what a caller reads when the Ranger's filtering Marker
// refuses its request (spec 09 §2): the gateway answers it, and the engine
// recognizes it, as this exact constant, in a failed task's status message.
const RejectMessage = "rejected by the Ranger: a secret shape in the request"
