"use client";

import { createContext, useContext } from "react";

// The logged-in user's name, so session views can tell whether the viewer may change a session.
export const CurrentUserContext = createContext<string>("");

export const useCurrentUser = () => useContext(CurrentUserContext);
