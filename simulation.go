func runGameLoop() {
	InfoLog.Println("Starting Galaxy Engine (V3.1 Time Lord Mode)...")

	for {
		// 1. Calculate Offset based on Election/Rank
		offset := CalculateOffset()

		// 2. Determine Target Time (Global 5s Grid)
		// We align to the nearest 5-second mark (Unix Epoch)
		now := time.Now().UnixMilli()
		target := ((now / 5000) * 5000) + 5000 + offset.Milliseconds()

		// 3. Sleep until Target
		sleep := time.Until(time.UnixMilli(target))
		if sleep > 0 {
            // Optional: Log if sleep is large (debug)
			time.Sleep(sleep)
		}

		// 4. Execute Tick
		tickWorld()
	}
}
