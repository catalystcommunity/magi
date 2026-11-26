plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// The speech library and the voice models (Senses.kt) are not in the
// repository. tools/fetch-android-voice.sh puts them in this directory and
// checks their digests (android/voice-pins.txt).
val voiceDir: String = (findProperty("magiVoiceDir") as String?) ?: System.getenv("MAGI_ANDROID_VOICE")
    ?: "${System.getProperty("user.home")}/.cache/magi/android-voice"
if (!file("$voiceDir/voice-pins.txt").exists() || file("$voiceDir/voice-pins.txt").readText() != file("../voice-pins.txt").readText()) {
    throw GradleException("The speech library and the voice models in $voiceDir are missing or old. Run tools/fetch-android-voice.sh.")
}

android {
    namespace = "community.catalyst.magi"
    compileSdk = 36

    defaultConfig {
        applicationId = "community.catalyst.magi"
        minSdk = 29
        targetSdk = 36
        // tools/build-android.sh sets these; each build needs a higher code
        // for the self-update (Updater.kt).
        versionCode = (findProperty("magiVersionCode") as String?)?.toInt() ?: 1
        versionName = (findProperty("magiVersionName") as String?) ?: "0.1.0"
        // Phones are arm64. -PmagiEmulator adds the library for an emulator
        // on a computer.
        ndk { abiFilters += listOfNotNull("arm64-v8a", if (hasProperty("magiEmulator")) "x86_64" else null) }
    }
    // The models load faster from a package that does not compress them.
    androidResources { noCompress += "onnx" }
    buildTypes {
        getByName("debug") { applicationIdSuffix = ".dev" }
        getByName("release") { isMinifyEnabled = false }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    sourceSets["main"].java.srcDir("../../clients/kotlin")
    sourceSets["main"].assets.srcDir("$voiceDir/assets")
}

dependencies {
    implementation(files("$voiceDir/sherpa-onnx.aar"))
    testImplementation(kotlin("test"))
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.0")
}

tasks.withType<Test>().configureEach {
    useJUnitPlatform()
    // UnigramTest reads the piece model of the spotter.
    systemProperty("magi.voice", voiceDir)
}
