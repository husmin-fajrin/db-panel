-- Plan 5b-3a (spec 5.1, "Jendela kata sandi"): via_window is 1 for a
-- confirmation token issued without a password, inside the session's
-- ten-minute password window. Only light actions accept such a token;
-- heavy ones refuse it when it is used. Older tokens were all issued with
-- a password: 0.
ALTER TABLE confirmations ADD COLUMN via_window INTEGER NOT NULL DEFAULT 0;
